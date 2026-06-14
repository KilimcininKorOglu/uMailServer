package activesync

import (
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
)

// Ping status codes (MS-ASCMD 2.2.3.167.x Status (Ping)).
const (
	pingStatusNoChanges      = "1" // heartbeat expired with no change in the monitored folders
	pingStatusChanges        = "2" // a monitored folder changed; Folders lists the changed ids
	pingStatusMissingParams  = "3" // no HeartbeatInterval/Folders sent and none cached
	pingStatusHeartbeatRange = "5" // HeartbeatInterval out of range; response carries the clamp
)

// Heartbeat bounds in seconds. minHeartbeat/maxHeartbeat frame the hold a client
// may request; noDeadlineHeartbeat caps a single hold when the write deadline
// could not be lifted, so the response is written before the listener's 30s
// WriteTimeout and the client re-issues (the MS-ASCMD Ping reissue loop).
const (
	minHeartbeat        = 60
	maxHeartbeat        = 3540
	noDeadlineHeartbeat = 25
)

// heartbeatUnit is the real-time duration of one heartbeat second. It is a var
// only so tests can shrink the hold; production always uses one second.
var heartbeatUnit = time.Second

// pimPollInterval is how often a held Ping re-enumerates a monitored PIM
// collection (calendar/contacts/tasks) to detect a change. Those classes have
// no notification hub (unlike mail), so the only way to notice an add/change/
// delete mid-hold is to re-list and diff. A var so tests can shrink it; the
// cost is one folder enumeration per interval per held Ping.
var pimPollInterval = 30 * time.Second

// MailboxNotifier subscribes to a mailbox's change events for the Ping command.
// The server bridges the shared notification hub (the one that drives IMAP IDLE
// and webmail SSE) so the activesync package needs no imap import. Each event is
// the name of a mail folder that changed — the same string a mail collection
// uses as its EAS CollectionId — so a held Ping matches it directly.
type MailboxNotifier interface {
	Subscribe(email string) (events <-chan string, cancel func())
}

// pingFolder is one monitored collection named in a Ping request.
type pingFolder struct {
	id    string
	class string
}

// pingCache remembers each device's last heartbeat and folder list. MS-ASCMD
// lets a client send a bare Ping after the first full one — the server saves the
// heartbeat value, so only the folder list (or nothing) is needed afterward.
type pingCache struct {
	mu      sync.Mutex
	entries map[string]pingCacheEntry
}

type pingCacheEntry struct {
	heartbeat int
	folders   []pingFolder
}

func newPingCache() *pingCache { return &pingCache{entries: map[string]pingCacheEntry{}} }

func (c *pingCache) get(key string) pingCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key]
}

func (c *pingCache) put(key string, e pingCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = e
}

// handlePing implements the MS-ASCMD Ping command: a long-poll that holds the
// connection for the heartbeat interval and returns as soon as a monitored
// folder changes (Status 2, listing the changed ids) or the interval expires
// with nothing changed (Status 1). Mail folders are watched through the shared
// notification hub; PIM collections (calendar/contacts/tasks) are accepted but
// only wake on the heartbeat until the periodic diff lands (Faz F2).
func (s *Server) handlePing(ctx *Context) ([]byte, error) {
	deviceID := ctx.Request.URL.Query().Get("DeviceId")
	key := ctx.Email + "\x00" + deviceID

	heartbeat, folders := parsePing(ctx.Body)

	// A bare Ping replays the device's saved heartbeat and folder set.
	cached := s.pings.get(key)
	if heartbeat == 0 {
		heartbeat = cached.heartbeat
	}
	if len(folders) == 0 {
		folders = cached.folders
	}
	if len(folders) == 0 {
		// The client has never sent a folder list: it must issue the full request.
		return marshalPing(pingStatusMissingParams, 0, nil)
	}
	if heartbeat != 0 && (heartbeat < minHeartbeat || heartbeat > maxHeartbeat) {
		// Out of range: hand back the nearest acceptable interval, do not hold.
		return marshalPing(pingStatusHeartbeatRange, clampHeartbeat(heartbeat), nil)
	}
	if heartbeat == 0 {
		heartbeat = minHeartbeat
	}
	s.pings.put(key, pingCacheEntry{heartbeat: heartbeat, folders: folders})

	// Hold the response past the listener's write timeout. If the writer chain
	// does not support lifting the deadline (a middleware wraps it without
	// Unwrap), fall back to a sub-timeout hold and let the client re-issue.
	if err := http.NewResponseController(ctx.W).SetWriteDeadline(time.Time{}); err != nil && heartbeat > noDeadlineHeartbeat {
		heartbeat = noDeadlineHeartbeat
	}

	monitored := make(map[string]bool, len(folders))
	watchMail := false
	// PIM collections have no notification hub, so capture a serverId->etag
	// baseline per monitored PIM folder now and re-enumerate on a ticker. Only
	// folders that enumerate cleanly are watched; a folder that errors here is
	// left to the heartbeat rather than risking a spurious wake later.
	baselines := make(map[string]map[string]string)
	for _, f := range folders {
		monitored[f.id] = true
		if isPIMCollection(f.id) {
			if snap, ok := s.pimSnapshot(ctx.Email, f.id); ok {
				baselines[f.id] = snap
			}
		} else {
			watchMail = true
		}
	}

	var events <-chan string
	if s.notifier != nil && watchMail {
		ch, cancel := s.notifier.Subscribe(ctx.Email)
		defer cancel()
		events = ch
	}

	var pimTick <-chan time.Time
	if len(baselines) > 0 {
		ticker := time.NewTicker(pimPollInterval)
		defer ticker.Stop()
		pimTick = ticker.C
	}

	timer := time.NewTimer(time.Duration(heartbeat) * heartbeatUnit)
	defer timer.Stop()
	done := ctx.Request.Context().Done()

	for {
		select {
		case <-done:
			// The client disconnected; there is nothing to write back.
			return nil, nil
		case <-timer.C:
			return marshalPing(pingStatusNoChanges, 0, nil)
		case folder, ok := <-events:
			if !ok {
				events = nil // hub closed the subscription; fall back to the heartbeat
				continue
			}
			if monitored[folder] {
				return marshalPing(pingStatusChanges, 0, []string{folder})
			}
		case <-pimTick:
			// Re-enumerate each monitored PIM folder; a serverId->etag set that
			// differs from the baseline means an add, change or delete landed.
			// A transient enumeration error keeps the baseline and retries.
			var changed []string
			for id, base := range baselines {
				if snap, ok := s.pimSnapshot(ctx.Email, id); ok && !maps.Equal(snap, base) {
					changed = append(changed, id)
				}
			}
			if len(changed) > 0 {
				sort.Strings(changed)
				return marshalPing(pingStatusChanges, 0, changed)
			}
		}
	}
}

// pimSnapshot enumerates a monitored PIM collection and returns its
// serverId->etag set, the same identity the enumerate-and-diff Sync path uses,
// so a change the Ping detects is the same change the client's follow-up Sync
// reports (no spurious wake). The prefix dispatch mirrors handleSync exactly.
// ok is false when the collection is not a wired PIM source or enumeration
// fails; the caller then leaves the folder to the heartbeat.
func (s *Server) pimSnapshot(email, collectionID string) (map[string]string, bool) {
	if folderID, ok := strings.CutPrefix(collectionID, calendarCollectionPrefix); ok && s.calendar != nil {
		items, err := s.calendar.ListItems(email, folderID)
		if err != nil {
			return nil, false
		}
		snap := make(map[string]string, len(items))
		for _, it := range items {
			snap[it.ServerID] = it.ETag
		}
		return snap, true
	}
	if folderID, ok := strings.CutPrefix(collectionID, contactsCollectionPrefix); ok && s.contacts != nil {
		items, err := s.contacts.ListItems(email, folderID)
		if err != nil {
			return nil, false
		}
		snap := make(map[string]string, len(items))
		for _, it := range items {
			snap[it.ServerID] = it.ETag
		}
		return snap, true
	}
	if folderID, ok := strings.CutPrefix(collectionID, tasksCollectionPrefix); ok && s.tasks != nil {
		items, err := s.tasks.ListItems(email, folderID)
		if err != nil {
			return nil, false
		}
		snap := make(map[string]string, len(items))
		for _, it := range items {
			snap[it.ServerID] = it.ETag
		}
		return snap, true
	}
	return nil, false
}

// parsePing extracts the HeartbeatInterval (seconds, 0 when absent) and the
// monitored folder list from a Ping request body. A malformed or empty body
// yields zero values so the caller can fall back to the device's cached state.
func parsePing(body []byte) (heartbeat int, folders []pingFolder) {
	root, err := wbxml.Unmarshal(body)
	if err != nil || root == nil {
		return 0, nil
	}
	if hb := textOf(root.Sub("HeartbeatInterval")); hb != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(hb)); err == nil {
			heartbeat = n
		}
	}
	if fs := root.Sub("Folders"); fs != nil {
		for _, f := range fs.Children {
			if f.Name != "Folder" {
				continue
			}
			folders = append(folders, pingFolder{id: textOf(f.Sub("Id")), class: textOf(f.Sub("Class"))})
		}
	}
	return heartbeat, folders
}

// marshalPing builds a Ping response: the status, plus the changed collection
// ids in Folders for Status 2, or the clamped HeartbeatInterval for Status 5.
func marshalPing(status string, heartbeat int, changed []string) ([]byte, error) {
	root := &wbxml.Element{Page: wbxml.PagePing, Name: "Ping", Children: []*wbxml.Element{
		{Page: wbxml.PagePing, Name: "Status", Text: status},
	}}
	if status == pingStatusHeartbeatRange && heartbeat > 0 {
		root.Children = append(root.Children, &wbxml.Element{Page: wbxml.PagePing, Name: "HeartbeatInterval", Text: strconv.Itoa(heartbeat)})
	}
	if len(changed) > 0 {
		fs := &wbxml.Element{Page: wbxml.PagePing, Name: "Folders"}
		for _, id := range changed {
			fs.Children = append(fs.Children, &wbxml.Element{Page: wbxml.PagePing, Name: "Folder", Text: id})
		}
		root.Children = append(root.Children, fs)
	}
	return wbxml.Marshal(root)
}

// clampHeartbeat returns the nearest in-range heartbeat interval.
func clampHeartbeat(hb int) int {
	if hb < minHeartbeat {
		return minHeartbeat
	}
	if hb > maxHeartbeat {
		return maxHeartbeat
	}
	return hb
}

// isPIMCollection reports whether a CollectionId addresses a calendar, contacts,
// or tasks collection (the prefixed collab classes) rather than a mail folder.
func isPIMCollection(id string) bool {
	return strings.HasPrefix(id, calendarCollectionPrefix) ||
		strings.HasPrefix(id, contactsCollectionPrefix) ||
		strings.HasPrefix(id, tasksCollectionPrefix)
}

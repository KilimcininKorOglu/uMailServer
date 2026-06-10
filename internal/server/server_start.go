package server

import (
	"fmt"
	"path/filepath"

	"github.com/umailserver/umailserver/internal/imap"
	"github.com/umailserver/umailserver/internal/queue"
)

// Start starts all server components
func (s *Server) Start() error {
	s.logger.Info("Starting uMailServer",
		"hostname", s.cfg().Server.Hostname,
		"data_dir", s.cfg().Server.DataDir,
	)

	// Create PID file. The PID file guards against a second instance owning the
	// same data_dir on ONE host; in cluster mode the data_dir (shared Maildir) is
	// deliberately shared across nodes, so the guard is both wrong and harmful
	// (every node would see the first node's PID and refuse to start). Skip it
	// when clustered — multi-node single-instance safety is the canonical store's
	// job (Postgres), not a shared file lock.
	if !s.cfg().Cluster.Enabled {
		pidFile := NewPIDFile(s.cfg().Server.DataDir)
		if err := pidFile.Create(); err != nil {
			return fmt.Errorf("failed to create PID file: %w", err)
		}
		s.logger.Debug("PID file created")
	} else {
		s.logger.Info("cluster mode: skipping shared-data_dir PID file")
	}

	// Initialize queue manager
	queueDir := filepath.Join(s.cfg().Server.DataDir, "queue")
	s.queue = queue.NewManager(s.database, nil, queueDir, s.logger)
	s.queue.SetDiskSync(s.cfg().Storage.Sync)
	s.queue.SetTracingProvider(s.tracingProvider)
	s.queue.SetEgressIPFunc(s.resolveEgressIP)
	s.queue.Start(s.ctx)
	s.logger.Info("Queue manager started")

	// Wire webhook manager to queue for delivery events
	if s.webhookMgr != nil {
		s.queue.SetWebhookTrigger(s.webhookMgr)
	}

	// Create mailstore for IMAP using shared storage
	s.mailstore = imap.NewBboltMailstoreWithInterfaces(s.storageDB, s.msgStore)

	// Set MDN handler for read receipts
	s.mailstore.SetMDNHandler(s.sendMDN)

	// Wire canonical mutation pipeline for semantic identity assignment.
	// This enables unified message-mutation semantics for IMAP append/update
	// alongside the existing SMTP local delivery path.
	if s.mutationPipe != nil {
		s.mailstore.SetMutationPipeline(s.mutationPipe)
	}

	// Wire the canonical semcore identity store so IMAP EXPUNGE removes the
	// message's semantic identity too (the mutation pipeline only adds it),
	// keeping EWS FindItem from showing a ghost after a copy/move is expunged.
	if s.semcoreStore != nil {
		s.mailstore.SetIdentityStore(s.semcoreStore.Identity())
	}

	// Wire the soft-delete dumpster so an IMAP/POP3 EXPUNGE files the message into
	// Recoverable Items before unlinking it (self-guards on recoverable_items.enabled).
	s.mailstore.SetRecoverableCapture(s.captureForRecovery)

	s.startSMTP()

	// Start search indexing worker pool
	if s.searchSvc != nil {
		for i := 0; i < 10; i++ {
			s.wg.Add(1)
			go s.runIndexWorker()
		}
	}

	// Start vacation reply cleanup goroutine (time-based, runs hourly)
	s.startVacationCleanup()

	// Start alert checker goroutine (periodic health checks for alerting)
	s.startAlertChecker()

	// Start the EWS push-notification dispatcher (delivers PushSubscription
	// events to client callback URLs; leader-gated in a cluster).
	s.startEWSPushDispatcher()

	// Start the scheduled-send release loop (delivers "send later" messages at
	// their time; leader-gated in a cluster).
	s.startScheduledSender()

	// Start the recoverable-items retention cleaner (purges soft-deleted mail
	// past its window; leader-gated in a cluster). No-op when disabled.
	s.startRecoverableItemsCleaner()

	if err := s.startIMAP(s.mailstore); err != nil {
		return err
	}

	if err := s.startPOP3(s.mailstore); err != nil {
		return err
	}

	s.startMCP()
	s.startManageSieve()
	s.startCalDAV()
	s.startCardDAV()
	s.startJMAP()
	s.startAPI()
	s.startMetrics()

	// Enable live config reload from disk (file watch + SIGHUP) now that every
	// service and the admin API are up.
	s.startConfigReload()

	// Begin cluster leadership + heartbeat when clustering is enabled (no-op
	// otherwise).
	s.startClusterHeartbeat()

	// Bridge IMAP IDLE / webmail SSE notifications across the cluster (no-op
	// when un-clustered).
	s.startNotificationBridge()

	return nil
}

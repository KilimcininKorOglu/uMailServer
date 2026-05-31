import { useState, useEffect } from "react"
import { useParams, useNavigate } from "react-router-dom"
import {
  ArrowLeft,
  Trash2,
  Reply,
  ReplyAll,
  Forward,
  Mail,
  FolderInput,
  Flag,
  Tag,
  X,
  Plus,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import { sanitizeHTML } from "@/utils/sanitize"
import api from "@/utils/api"
import { useAuth } from "@/contexts/AuthContext"

interface EmailDetail {
  id: string
  from: string
  fromEmail: string
  to: string[]
  subject: string
  date: string
  content: string
  flagged: boolean
  labels: string[]
}

export function EmailDetailPage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const [email, setEmail] = useState<EmailDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [newLabel, setNewLabel] = useState("")
  const [labelEditing, setLabelEditing] = useState(false)

  // Load the message by id (the backend resolves it across all folders).
  useEffect(() => {
    const loadEmail = async () => {
      if (!id) {
        setLoading(false)
        return
      }
      try {
        setLoading(true)
        const result = await api.getMessage(id)
        if (result && result.id) {
          const fromParts = (result.from || "").split("<")
          const fromEmail =
            fromParts.length > 1 ? fromParts[1].replace(">", "").trim() : result.from
          const fromName = fromParts.length > 1 ? fromParts[0].trim() : result.from
          setEmail({
            id: result.id,
            from: fromName || fromEmail,
            fromEmail,
            to: result.to ?? [],
            subject: result.subject,
            date: result.date,
            content: result.body,
            flagged: !!result.starred,
            labels: result.labels ?? [],
          })
        } else {
          toast.error("Email not found")
          navigate("/inbox")
        }
      } catch (err) {
        console.error("Failed to load email:", err)
        toast.error("Failed to load email")
        navigate("/inbox")
      } finally {
        setLoading(false)
      }
    }
    loadEmail()
  }, [id, navigate])

  const handleDelete = async () => {
    if (!email) return
    try {
      await api.deleteMail(email.id)
      toast.success("Email moved to trash")
      navigate("/inbox")
    } catch {
      toast.error("Failed to delete email")
    }
  }

  const handleReply = () => {
    if (!email) return
    const params = new URLSearchParams({
      replyTo: email.fromEmail,
      subject: email.subject.startsWith("Re: ") ? email.subject : `Re: ${email.subject}`,
    })
    navigate(`/compose?${params.toString()}`)
  }

  const handleReplyAll = () => {
    if (!email) return
    const self = user?.email?.toLowerCase()
    // Other To recipients become Cc, excluding the original sender and ourselves.
    const others = email.to
      .map((t) => {
        const m = t.match(/<([^>]+)>/)
        return (m ? m[1] : t).trim()
      })
      .filter(
        (e) =>
          e &&
          e.toLowerCase() !== self &&
          e.toLowerCase() !== email.fromEmail.toLowerCase()
      )
    const params = new URLSearchParams({
      replyTo: email.fromEmail,
      subject: email.subject.startsWith("Re: ") ? email.subject : `Re: ${email.subject}`,
    })
    if (others.length > 0) params.set("cc", others.join(","))
    navigate(`/compose?${params.toString()}`)
  }

  const handleForward = () => {
    if (!email) return
    const quoted = `\n\n---------- Forwarded message ----------\nFrom: ${email.from} <${email.fromEmail}>\nDate: ${email.date}\nSubject: ${email.subject}\nTo: ${email.to.join(", ")}\n\n${email.content}`
    const params = new URLSearchParams({
      subject: email.subject.startsWith("Fwd: ") ? email.subject : `Fwd: ${email.subject}`,
      body: quoted,
    })
    navigate(`/compose?${params.toString()}`)
  }

  const handleMarkUnread = async () => {
    if (!email) return
    try {
      await api.setFlag(email.id, "\\Seen", false)
      toast.success("Marked as unread")
      navigate("/inbox")
    } catch {
      toast.error("Failed to mark as unread")
    }
  }

  // handleToggleFollowUp flags/unflags the message for follow-up. This is the
  // IMAP \Flagged flag — the same primitive Outlook/EWS exposes as a follow-up
  // flag (and what the list view's star uses), surfaced here in the reading view.
  const handleToggleFollowUp = async () => {
    if (!email) return
    const next = !email.flagged
    setEmail({ ...email, flagged: next })
    try {
      await api.setFlag(email.id, "\\Flagged", next)
      toast.success(next ? "Flagged for follow-up" : "Follow-up flag cleared")
    } catch {
      setEmail({ ...email, flagged: !next })
      toast.error("Failed to update follow-up flag")
    }
  }

  // saveLabels persists the full label set and updates state on success.
  const saveLabels = async (next: string[]) => {
    if (!email) return
    const prev = email.labels
    setEmail({ ...email, labels: next })
    try {
      await api.setMailLabels(email.id, next)
    } catch {
      setEmail({ ...email, labels: prev })
      toast.error("Failed to update labels")
    }
  }

  const handleAddLabel = () => {
    if (!email) return
    const value = newLabel.trim()
    if (!value || email.labels.includes(value)) {
      setNewLabel("")
      return
    }
    setNewLabel("")
    void saveLabels([...email.labels, value])
  }

  const handleRemoveLabel = (label: string) => {
    if (!email) return
    void saveLabels(email.labels.filter((l) => l !== label))
  }

  const handleMove = async (folder: string, label: string) => {
    if (!email) return
    try {
      await api.moveMail(email.id, folder)
      toast.success(`Moved to ${label}`)
      navigate("/inbox")
    } catch {
      toast.error("Failed to move message")
    }
  }

  return (
    <div className="space-y-4">
      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
        </div>
      ) : !email ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <h3 className="mt-4 text-lg font-semibold">Email not found</h3>
          <p className="text-sm text-muted-foreground">This email may have been deleted or moved.</p>
          <Button className="mt-4" onClick={() => navigate("/inbox")}>Back to Inbox</Button>
        </div>
      ) : (
        <>
          {/* Toolbar */}
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-1">
              <Button variant="ghost" size="icon" onClick={() => navigate(-1)} title="Back">
                <ArrowLeft className="h-5 w-5" />
              </Button>
              <Button variant="ghost" size="sm" onClick={handleReply} title="Reply">
                <Reply className="h-4 w-4 mr-1" />
                Reply
              </Button>
              <Button variant="ghost" size="sm" onClick={handleReplyAll} title="Reply all">
                <ReplyAll className="h-4 w-4 mr-1" />
                Reply all
              </Button>
              <Button variant="ghost" size="sm" onClick={handleForward} title="Forward">
                <Forward className="h-4 w-4 mr-1" />
                Forward
              </Button>
            </div>
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon"
                onClick={handleToggleFollowUp}
                title={email.flagged ? "Clear follow-up flag" : "Flag for follow-up"}
                aria-pressed={email.flagged}
              >
                <Flag className={email.flagged ? "h-5 w-5 fill-amber-500 text-amber-500" : "h-5 w-5"} />
              </Button>
              <Button variant="ghost" size="icon" onClick={handleMarkUnread} title="Mark as unread">
                <Mail className="h-5 w-5" />
              </Button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" title="Move to folder">
                    <FolderInput className="h-5 w-5" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onClick={() => handleMove("inbox", "Inbox")}>Inbox</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("archive", "Archive")}>Archive</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("spam", "Spam")}>Spam</DropdownMenuItem>
                  <DropdownMenuItem onClick={() => handleMove("trash", "Trash")}>Trash</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                variant="ghost"
                size="icon"
                className="text-destructive"
                onClick={handleDelete}
                title="Delete"
              >
                <Trash2 className="h-5 w-5" />
              </Button>
            </div>
          </div>

          {/* Email Content */}
          <div className="rounded-lg border bg-card">
            {/* Header */}
            <div className="p-6 pb-0">
              <h1 className="text-2xl font-semibold leading-tight">{email.subject}</h1>

              <div className="flex items-start gap-4 mt-6">
                <Avatar className="h-12 w-12 ring-2 ring-primary/10">
                  <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold text-lg">
                    {email.from.split(" ").map((n) => n[0]).join("").slice(0, 2)}
                  </AvatarFallback>
                </Avatar>

                <div className="flex-1 min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-semibold text-lg">{email.from}</span>
                    <span className="text-sm text-muted-foreground">
                      &lt;{email.fromEmail}&gt;
                    </span>
                  </div>

                  <div className="mt-1 text-sm text-muted-foreground">
                    <span className="font-medium text-foreground">To:</span> {email.to.join(", ")}
                  </div>

                  <div className="mt-1 text-sm text-muted-foreground">{email.date}</div>

                  {/* Category labels */}
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    <Tag className="h-3.5 w-3.5 text-muted-foreground" />
                    {email.labels.map((label) => (
                      <Badge key={label} variant="secondary" className="gap-1">
                        {label}
                        <button
                          onClick={() => handleRemoveLabel(label)}
                          className="text-muted-foreground hover:text-destructive"
                          aria-label={`Remove ${label}`}
                        >
                          <X className="h-3 w-3" />
                        </button>
                      </Badge>
                    ))}
                    {labelEditing ? (
                      <Input
                        autoFocus
                        value={newLabel}
                        onChange={(e) => setNewLabel(e.target.value)}
                        onBlur={() => { handleAddLabel(); setLabelEditing(false) }}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") { handleAddLabel(); setLabelEditing(false) }
                          if (e.key === "Escape") { setNewLabel(""); setLabelEditing(false) }
                        }}
                        placeholder="Label"
                        className="h-6 w-28 text-xs"
                      />
                    ) : (
                      <button
                        onClick={() => setLabelEditing(true)}
                        className="flex items-center gap-1 rounded border border-dashed px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground"
                      >
                        <Plus className="h-3 w-3" />
                        Add label
                      </button>
                    )}
                  </div>
                </div>
              </div>
            </div>

            <Separator className="my-6" />

            {/* Body */}
            <div className="px-6 pb-6">
              <div
                className="prose prose-neutral dark:prose-invert max-w-none prose-headings:font-semibold prose-p:leading-relaxed prose-ul:leading-relaxed whitespace-pre-wrap"
                dangerouslySetInnerHTML={{ __html: sanitizeHTML(email.content) }}
              />
            </div>
          </div>
        </>
      )}
    </div>
  )
}

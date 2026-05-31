import { useState, useEffect } from "react"
import { useParams, useNavigate } from "react-router-dom"
import {
  ArrowLeft,
  Archive,
  Trash2,
  MoreHorizontal,
  Paperclip,
  Star,
  Printer,
  Download,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import { sanitizeHTML } from "@/utils/sanitize"
import api, { Mail } from "@/utils/api"

interface Attachment {
  name: string
  size: string
  type: string
}

interface EmailDetail {
  id: string
  from: string
  fromEmail: string
  to: string[]
  cc?: string[]
  subject: string
  date: string
  content: string
  starred: boolean
  attachments?: Attachment[]
}

export function EmailDetailPage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const [email, setEmail] = useState<EmailDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [isStarred, setIsStarred] = useState(false)
  const [folder] = useState("inbox")
  
  // Load email from API
  useEffect(() => {
    const loadEmail = async () => {
      if (!id) {
        setLoading(false)
        return
      }
      
      try {
        setLoading(true)
        // Extract folder from URL path for proper API call
        // The email id contains info about which folder it belongs to
        // For now, try inbox as default
        const result = await api.get<Mail>(`/mail/${folder}?id=${id}`)
        
        if (result && result.id) {
          // Parse email address from From field
          const fromParts = result.from.split('<')
          const fromEmail = fromParts.length > 1 ? fromParts[1].replace('>', '') : result.from
          const fromName = fromParts.length > 1 ? fromParts[0].trim() : result.from
          
          setEmail({
            id: result.id,
            from: fromName,
            fromEmail: fromEmail,
            to: result.to,
            subject: result.subject,
            date: result.date,
            content: result.body,
            starred: result.starred,
            attachments: result.hasAttachments ? [] : undefined,
          })
          setIsStarred(result.starred)
        } else {
          toast.error("Email not found")
          navigate("/inbox")
        }
      } catch (err) {
        console.error('Failed to load email:', err)
        toast.error("Failed to load email")
        navigate("/inbox")
      } finally {
        setLoading(false)
      }
    }
    
    loadEmail()
  }, [id, folder, navigate])

  const handleArchive = () => {
    toast.success("Email archived")
  }

  const handleDelete = () => {
    toast.success("Email moved to trash")
    navigate("/trash")
  }

  const handleDownload = (attachment: Attachment) => {
    toast.success(`Downloading ${attachment.name}`)
  }

  const handleToggleStar = () => {
    setIsStarred(!isStarred)
    toast.success(isStarred ? "Star removed" : "Starred")
  }

  const getAttachmentIcon = (type: string) => {
    switch (type) {
      case "pdf":
        return "📄"
      case "xlsx":
      case "xls":
        return "📊"
      case "docx":
      case "doc":
        return "📝"
      case "jpg":
      case "png":
      case "gif":
        return "🖼️"
      case "zip":
      case "rar":
        return "📦"
      default:
        return "📎"
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
          <div className="rounded-full bg-muted p-4">
            <Archive className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">Email not found</h3>
          <p className="text-sm text-muted-foreground">This email may have been deleted or moved.</p>
          <Button className="mt-4" onClick={() => navigate("/inbox")}>Back to Inbox</Button>
        </div>
      ) : (
        <>
      {/* Toolbar */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="icon" onClick={() => navigate(-1)}>
            <ArrowLeft className="h-5 w-5" />
          </Button>
          <Button variant="ghost" size="icon" onClick={handleArchive} title="Archive">
            <Archive className="h-5 w-5" />
          </Button>
          <Button variant="ghost" size="icon" className="text-destructive" onClick={handleDelete} title="Delete">
            <Trash2 className="h-5 w-5" />
          </Button>
        </div>

        <div className="flex items-center gap-1">
          <Button variant="ghost" size="icon" onClick={handleToggleStar} title={isStarred ? "Remove star" : "Star"}>
            <Star className={cn("h-5 w-5", isStarred && "fill-amber-400 text-amber-400")} />
          </Button>
          <Button variant="ghost" size="icon" title="Print">
            <Printer className="h-5 w-5" />
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon">
                <MoreHorizontal className="h-5 w-5" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem>View headers</DropdownMenuItem>
              <DropdownMenuItem>Open in new tab</DropdownMenuItem>
              <DropdownMenuItem>Show original</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-destructive">
                <Trash2 className="mr-2 h-4 w-4" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {/* Email Content */}
      <div className="rounded-lg border bg-card">
        {/* Header */}
        <div className="p-6 pb-0">
          <div className="flex items-start justify-between gap-4">
            <h1 className="text-2xl font-semibold leading-tight">{email.subject}</h1>
          </div>

          <div className="flex items-start gap-4 mt-6">
            <Avatar className="h-12 w-12 ring-2 ring-primary/10">
              <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold text-lg">
                {email.from.split(" ").map((n) => n[0]).join("")}
              </AvatarFallback>
            </Avatar>

            <div className="flex-1 min-w-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-semibold text-lg">{email.from}</span>
                <span className="text-sm text-muted-foreground">
                  &lt;{email.fromEmail}&gt;
                </span>
              </div>

              <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
                <span>
                  <span className="font-medium text-foreground">To:</span> {email.to.join(", ")}
                </span>
                {email.cc && (
                  <span>
                    <span className="font-medium text-foreground">Cc:</span> {email.cc.join(", ")}
                  </span>
                )}
              </div>

              <div className="mt-1 text-sm text-muted-foreground">
                {email.date}
              </div>
            </div>
          </div>
        </div>

        <Separator className="my-6" />

        {/* Body */}
        <div className="px-6 pb-6">
          <div
            className="prose prose-neutral dark:prose-invert max-w-none prose-headings:font-semibold prose-p:leading-relaxed prose-ul:leading-relaxed"
            dangerouslySetInnerHTML={{ __html: sanitizeHTML(email.content) }}
          />
        </div>

        {/* Attachments */}
        {email.attachments && email.attachments.length > 0 && (
          <>
            <Separator className="my-6" />
            <div className="px-6 pb-6">
              <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
                <Paperclip className="h-4 w-4" />
                Attachments ({email.attachments.length})
              </h3>
              <div className="flex flex-wrap gap-3">
                {email.attachments.map((attachment) => (
                  <div
                    key={attachment.name}
                    className="flex items-center gap-3 rounded-lg border bg-muted/50 hover:bg-muted transition-colors cursor-pointer group"
                    onClick={() => handleDownload(attachment)}
                  >
                    <span className="text-2xl pl-3">{getAttachmentIcon(attachment.type)}</span>
                    <div className="py-3 pr-4">
                      <p className="text-sm font-medium group-hover:text-primary transition-colors">
                        {attachment.name}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {attachment.size}
                      </p>
                    </div>
                    <Button variant="ghost" size="icon" className="h-8 w-8 mr-2 opacity-0 group-hover:opacity-100">
                      <Download className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          </>
        )}
      </div>
        </>
      )}
    </div>
  )
}

function cn(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(" ")
}

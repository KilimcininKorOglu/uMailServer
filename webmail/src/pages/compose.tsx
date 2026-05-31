import { useState, useRef, useEffect, useCallback } from "react"
import { useNavigate, useSearchParams } from "react-router-dom"
import {
  ArrowLeft,
  Send,
  Save,
  Paperclip,
  X,
  Plus,
  Bold,
  Italic,
  Underline,
  Link,
  List,
  Image,
  Minimize2,
  Maximize2,
  Clock,
  Check,
  AlertTriangle,
  Mail,
  ChevronDown,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import api, { SenderIdentity, DiagnosticEntry, Contact as ContactType } from "@/utils/api"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"

interface Attachment {
  id: string
  name: string
  size: number
  file?: File
}

interface Recipient {
  id: string
  name: string
  email: string
}

export function ComposePage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { user } = useAuth()
  const { currentMailbox, isInSharedMailbox } = useMailbox()
  
  const [to, setTo] = useState<Recipient[]>([])
  const [cc, setCc] = useState<Recipient[]>([])
  const [bcc, setBcc] = useState<Recipient[]>([])
  const [isFullscreen, setIsFullscreen] = useState(false)
  const [lastSaved, setLastSaved] = useState<Date | null>(null)
  const [isSaving, setIsSaving] = useState(false)
  
  // Sender identity state
  const [senderIdentities, setSenderIdentities] = useState<SenderIdentity[]>([])
  const [selectedSender, setSelectedSender] = useState<SenderIdentity | null>(null)
  const [showSenderDropdown, setShowSenderDropdown] = useState(false)
  const [diagnostics, setDiagnostics] = useState<DiagnosticEntry[]>([])
  const [showDiagnostics, setShowDiagnostics] = useState(false)
  
  // Load sender identities on mount
  useEffect(() => {
    const loadSenderIdentities = async () => {
      try {
        const identities = await api.getSenderIdentities(user?.email || '')
        setSenderIdentities(identities)
        
        // Set default sender based on current mailbox context
        if (isInSharedMailbox() && currentMailbox.owner) {
          // Default to the shared mailbox identity when in shared context
          const sharedIdentity = identities.find(
            (id: SenderIdentity) => id.email === currentMailbox.owner && id.type !== 'personal'
          )
          if (sharedIdentity) {
            setSelectedSender(sharedIdentity)
          } else if (identities.length > 0) {
            setSelectedSender(identities[0])
          }
        } else if (identities.length > 0) {
          // Default to personal identity
          const personalIdentity = identities.find((id: SenderIdentity) => id.type === 'personal')
          setSelectedSender(personalIdentity || identities[0])
        }
      } catch (err) {
        console.error('Failed to load sender identities:', err)
        // Fallback to personal identity
        if (user?.email) {
          setSelectedSender({
            email: user.email,
            displayName: user.email,
            type: 'personal',
            canSend: true
          })
        }
      }
    }
    
    loadSenderIdentities()
  }, [user, currentMailbox, isInSharedMailbox])
  
  // Load diagnostics
  useEffect(() => {
    const loadDiagnostics = async () => {
      try {
        const result = await api.getDiagnostics()
        if (result.errors) {
          setDiagnostics(result.errors)
        }
      } catch (err) {
        console.error('Failed to load diagnostics:', err)
      }
    }
    
    loadDiagnostics()
  }, [])

  const [subject, setSubject] = useState("")
  const [body, setBody] = useState("")
  const [attachments, setAttachments] = useState<Attachment[]>([])
  const [searchQuery, setSearchQuery] = useState("")
  const [showCc, setShowCc] = useState(false)
  const [showBcc, setShowBcc] = useState(false)
  const [sending, setSending] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const autoSaveTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  
  // Contacts loaded from API for recipient selection
  const [contacts, setContacts] = useState<Recipient[]>([])
  
  // Load contacts from API on mount
  useEffect(() => {
    const loadContacts = async () => {
      try {
        const result = await api.getContacts()
        if (result.contacts) {
          const recipients: Recipient[] = result.contacts.map((c: ContactType) => ({
            id: c.id,
            name: c.name,
            email: c.email,
          }))
          setContacts(recipients)
        }
      } catch (err) {
        console.error('Failed to load contacts:', err)
      }
    }
    loadContacts()
  }, [])
  
  // Handle replyTo/cc params after contacts are loaded. Both accept a
  // comma-separated list so Reply All can prefill multiple recipients.
  useEffect(() => {
    const toRecipient = (email: string, idx: number): Recipient => {
      const contact = contacts.find((c) => c.email === email)
      return contact ?? { id: `param-${idx}-${email}`, name: email, email }
    }
    const parseList = (raw: string | null) =>
      (raw ?? "")
        .split(",")
        .map((e) => e.trim())
        .filter(Boolean)

    const replyTo = parseList(searchParams.get("replyTo"))
    if (replyTo.length > 0) {
      setTo(replyTo.map(toRecipient))
    }
    const ccList = parseList(searchParams.get("cc"))
    if (ccList.length > 0) {
      setCc(ccList.map(toRecipient))
      setShowCc(true)
    }
  }, [searchParams, contacts])

  // Prefill subject/body from query params (used by reply and forward).
  useEffect(() => {
    const subjectParam = searchParams.get("subject")
    if (subjectParam) {
      setSubject(subjectParam)
    }
    const bodyParam = searchParams.get("body")
    if (bodyParam) {
      setBody(bodyParam)
    }
  }, [searchParams])

  const filteredContacts = contacts.filter(
    (c) =>
      c.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      c.email.toLowerCase().includes(searchQuery.toLowerCase())
  )

  const addRecipient = (contact: Recipient, field: "to" | "cc" | "bcc") => {
    if (field === "to") {
      if (!to.find((r) => r.id === contact.id)) {
        setTo([...to, contact])
      }
    } else if (field === "cc") {
      if (!cc.find((r) => r.id === contact.id)) {
        setCc([...cc, contact])
      }
    } else {
      if (!bcc.find((r) => r.id === contact.id)) {
        setBcc([...bcc, contact])
      }
    }
    setSearchQuery("")
  }

  const removeRecipient = (id: string, field: "to" | "cc" | "bcc") => {
    if (field === "to") {
      setTo(to.filter((r) => r.id !== id))
    } else if (field === "cc") {
      setCc(cc.filter((r) => r.id !== id))
    } else {
      setBcc(bcc.filter((r) => r.id !== id))
    }
  }

  const handleAttach = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (files) {
      const newAttachments = Array.from(files).map((file) => ({
        id: crypto.randomUUID(),
        name: file.name,
        size: file.size,
        file,
      }))
      setAttachments([...attachments, ...newAttachments])
      toast.success(`${files.length} file${files.length > 1 ? "s" : ""} attached`)
    }
  }

  const removeAttachment = (id: string) => {
    setAttachments(attachments.filter((a) => a.id !== id))
  }

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return bytes + " B"
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB"
    return (bytes / (1024 * 1024)).toFixed(1) + " MB"
  }

  const handleAutoSave = useCallback(() => {
    if (subject || body || to.length > 0 || attachments.length > 0) {
      setIsSaving(true)
      setTimeout(() => {
        setLastSaved(new Date())
        setIsSaving(false)
      }, 500)
    }
  }, [subject, body, to, attachments])

  useEffect(() => {
    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current)
    }
    autoSaveTimerRef.current = setTimeout(handleAutoSave, 3000)
    return () => {
      if (autoSaveTimerRef.current) {
        clearTimeout(autoSaveTimerRef.current)
      }
    }
  }, [subject, body, to, attachments, handleAutoSave])

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault()
        handleSend()
      }
    }
    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [to, subject, body])

  // Check if selected sender can send
  const canSendAsSelected = selectedSender?.canSend ?? true
  const sendError = !canSendAsSelected && selectedSender
    ? `You don't have permission to send as ${selectedSender.email}. Contact the mailbox owner for send-as or send-on-behalf access.`
    : null
  
  const handleSend = async () => {
    if (to.length === 0) {
      toast.error("Please select a recipient")
      return
    }
    if (!subject.trim()) {
      toast.error("Please enter a subject")
      return
    }
    
    // Check if sender is allowed
    if (!canSendAsSelected) {
      toast.error(sendError || "Cannot send with selected identity")
      return
    }
    
    // Check for policy errors from diagnostics
    const policyErrors = diagnostics.filter(d => d.category === 'policy' && d.severity === 'error')
    if (policyErrors.length > 0) {
      setShowDiagnostics(true)
      toast.error("Please resolve the issues before sending")
      return
    }
    
    setSending(true)
    toast.success("Sending email...")
    
    try {
      // Use the actual API with sender identity
      const senderEmail = selectedSender?.email || user?.email || ''
      await api.sendMail({
        to: to.map(r => r.email),
        cc: cc.map(r => r.email),
        bcc: bcc.map(r => r.email),
        subject,
        body,
        from: senderEmail, // Pass sender identity to API
      })
      
      toast.success("Email sent successfully")
      navigate("/sent")
    } catch (err) {
      console.error('Failed to send email:', err)
      toast.error("Failed to send email. Please try again.")
      setSending(false)
    }
  }

  const handleSaveDraft = () => {
    handleAutoSave()
    toast.success("Draft saved")
    navigate("/drafts")
  }

  const handleDiscard = () => {
    if (subject || body || to.length > 0) {
      if (confirm("Discard this email? Your draft will be saved.")) {
        handleSaveDraft()
      }
    } else {
      navigate("/inbox")
    }
  }

  const formatLastSaved = () => {
    if (!lastSaved) return null
    const now = new Date()
    const diff = Math.floor((now.getTime() - lastSaved.getTime()) / 1000)
    if (diff < 60) return "Just now"
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
    return lastSaved.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
  }

  return (
    <div className={cn(
      "flex flex-col bg-background transition-all duration-200",
      isFullscreen ? "fixed inset-0 z-50" : "h-[calc(100vh-4rem)]"
    )}>
      {/* Header */}
      <div className="flex items-center justify-between border-b px-4 py-2">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" onClick={handleDiscard}>
            <ArrowLeft className="h-5 w-5" />
          </Button>
          <span className="font-medium">New Message</span>
          {lastSaved && (
            <span className="flex items-center gap-1 text-xs text-muted-foreground ml-2">
              {isSaving ? (
                <>
                  <Clock className="h-3 w-3 animate-pulse" />
                  Saving...
                </>
              ) : (
                <>
                  <Check className="h-3 w-3" />
                  Saved {formatLastSaved()}
                </>
              )}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => setIsFullscreen(!isFullscreen)}
            title={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
          >
            {isFullscreen ? (
              <Minimize2 className="h-4 w-4" />
            ) : (
              <Maximize2 className="h-4 w-4" />
            )}
          </Button>
          <Button variant="ghost" size="icon" onClick={handleSaveDraft} title="Save draft (⌘S)">
            <Save className="h-4 w-4" />
          </Button>
          <Button
            className="gap-2"
            onClick={handleSend}
            disabled={sending || to.length === 0}
          >
            <Send className="h-4 w-4" />
            {sending ? "Sending..." : "Send"}
          </Button>
        </div>
      </div>

      {/* Recipients */}
      <div className="border-b px-4 py-2 space-y-2">
        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground">To:</span>
          <div className="flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
            {to.map((r) => (
              <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                {r.name}
                <button
                  onClick={() => removeRecipient(r.id, "to")}
                  className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" className="h-6 w-6">
                  <Plus className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-72">
                <div className="p-2">
                  <Input
                    placeholder="Search contacts..."
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                  />
                </div>
                <Separator />
                <div className="max-h-48 overflow-auto">
                  {filteredContacts.map((contact) => (
                    <DropdownMenuItem
                      key={contact.id}
                      onClick={() => addRecipient(contact, "to")}
                      className="flex flex-col items-start py-2"
                    >
                      <span className="font-medium">{contact.name}</span>
                      <span className="text-xs text-muted-foreground">
                        {contact.email}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </div>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="text-xs h-7"
            onClick={() => setShowCc(!showCc)}
          >
            Cc
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="text-xs h-7"
            onClick={() => setShowBcc(!showBcc)}
          >
            Bcc
          </Button>
        </div>

        {showCc && (
          <div className="flex items-center gap-2">
            <span className="w-12 text-sm text-muted-foreground">Cc:</span>
            <div className="flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
              {cc.map((r) => (
                <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                  {r.name}
                  <button
                    onClick={() => removeRecipient(r.id, "cc")}
                    className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" className="h-6 w-6">
                    <Plus className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-72">
                  <div className="p-2">
                    <Input
                      placeholder="Search contacts..."
                      value={searchQuery}
                      onChange={(e) => setSearchQuery(e.target.value)}
                    />
                  </div>
                  <Separator />
                  <div className="max-h-48 overflow-auto">
                    {filteredContacts.map((contact) => (
                      <DropdownMenuItem
                        key={contact.id}
                        onClick={() => addRecipient(contact, "cc")}
                        className="flex flex-col items-start py-2"
                      >
                        <span className="font-medium">{contact.name}</span>
                        <span className="text-xs text-muted-foreground">
                          {contact.email}
                        </span>
                      </DropdownMenuItem>
                    ))}
                  </div>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
        )}

        {showBcc && (
          <div className="flex items-center gap-2">
            <span className="w-12 text-sm text-muted-foreground">Bcc:</span>
            <div className="flex flex-1 flex-wrap items-center gap-1 min-h-[32px]">
              {bcc.map((r) => (
                <Badge key={r.id} variant="secondary" className="gap-1 pr-1.5 py-1">
                  {r.name}
                  <button
                    onClick={() => removeRecipient(r.id, "bcc")}
                    className="ml-0.5 rounded-full hover:bg-muted p-0.5"
                  >
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="ghost" size="icon" className="h-6 w-6">
                    <Plus className="h-4 w-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-72">
                  <div className="p-2">
                    <Input
                      placeholder="Search contacts..."
                      value={searchQuery}
                      onChange={(e) => setSearchQuery(e.target.value)}
                    />
                  </div>
                  <Separator />
                  <div className="max-h-48 overflow-auto">
                    {filteredContacts.map((contact) => (
                      <DropdownMenuItem
                        key={contact.id}
                        onClick={() => addRecipient(contact, "bcc")}
                        className="flex flex-col items-start py-2"
                      >
                        <span className="font-medium">{contact.name}</span>
                        <span className="text-xs text-muted-foreground">
                          {contact.email}
                        </span>
                      </DropdownMenuItem>
                    ))}
                  </div>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
        )}
        
        {/* Sender Identity Selector */}
        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground flex items-center gap-1">
            <Mail className="h-3 w-3" />
            From:
          </span>
          <div className="flex-1 flex items-center gap-2">
            <DropdownMenu open={showSenderDropdown} onOpenChange={setShowSenderDropdown}>
              <DropdownMenuTrigger asChild>
                <Button 
                  variant="outline" 
                  size="sm" 
                  className={cn(
                    "h-7 text-xs gap-1",
                    !canSendAsSelected && "border-red-500 text-red-500"
                  )}
                >
                  {selectedSender ? (
                    <>
                      <span className="truncate max-w-[150px]">
                        {selectedSender.displayName || selectedSender.email}
                      </span>
                      {selectedSender.type !== 'personal' && (
                        <Badge variant="secondary" className="text-[10px] h-4 ml-1">
                          {selectedSender.type === 'send-on-behalf' ? 'On behalf' : 'Send as'}
                        </Badge>
                      )}
                    </>
                  ) : (
                    <span>Select sender</span>
                  )}
                  <ChevronDown className="h-3 w-3 ml-1" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-80">
                <div className="p-2 text-xs text-muted-foreground">
                  Select the sender identity for this message
                </div>
                <Separator />
                <div className="max-h-60 overflow-auto">
                  {senderIdentities.map((identity) => (
                    <DropdownMenuItem
                      key={identity.email}
                      onClick={() => {
                        setSelectedSender(identity)
                        setShowSenderDropdown(false)
                      }}
                      className={cn(
                        "flex flex-col items-start py-2 cursor-pointer",
                        !identity.canSend && "opacity-50"
                      )}
                      disabled={!identity.canSend}
                    >
                      <div className="flex items-center gap-2 w-full">
                        <span className="font-medium text-sm">{identity.displayName || identity.email}</span>
                        {identity.type === 'personal' && (
                          <Badge variant="default" className="text-[10px] h-4">Personal</Badge>
                        )}
                        {identity.type === 'send-on-behalf' && (
                          <Badge variant="secondary" className="text-[10px] h-4">On behalf</Badge>
                        )}
                        {identity.type === 'send-as' && (
                          <Badge variant="outline" className="text-[10px] h-4">Send as</Badge>
                        )}
                      </div>
                      {identity.mailboxOwner && (
                        <span className="text-xs text-muted-foreground">
                          Shared mailbox: {identity.mailboxOwner}
                        </span>
                      )}
                      {!identity.canSend && (
                        <span className="text-xs text-red-500 flex items-center gap-1 mt-1">
                          <AlertTriangle className="h-3 w-3" />
                          No send permission
                        </span>
                      )}
                    </DropdownMenuItem>
                  ))}
                </div>
              </DropdownMenuContent>
            </DropdownMenu>
            
            {/* Show permission error inline */}
            {sendError && (
              <div className="flex items-center gap-1 text-xs text-red-500">
                <AlertTriangle className="h-3 w-3" />
                <span className="truncate max-w-[200px]">{sendError}</span>
              </div>
            )}
            
            {/* Diagnostics toggle */}
            {diagnostics.length > 0 && (
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6"
                onClick={() => setShowDiagnostics(!showDiagnostics)}
                title="View mail diagnostics"
              >
                <AlertTriangle className={cn(
                  "h-4 w-4",
                  diagnostics.some(d => d.severity === 'error') && "text-red-500"
                )} />
              </Button>
            )}
          </div>
        </div>
        
        {/* Diagnostics Panel */}
        {showDiagnostics && diagnostics.length > 0 && (
          <div className="border rounded-md bg-muted/30 p-3 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">Mailbox Diagnostics</span>
              <Button
                variant="ghost"
                size="icon"
                className="h-5 w-5"
                onClick={() => setShowDiagnostics(false)}
              >
                <X className="h-3 w-3" />
              </Button>
            </div>
            {diagnostics.map((entry) => (
              <div 
                key={entry.id}
                className={cn(
                  "text-xs p-2 rounded border-l-2",
                  entry.severity === 'error' && "bg-red-50 border-red-500 text-red-700 dark:bg-red-950 dark:text-red-400",
                  entry.severity === 'warning' && "bg-yellow-50 border-yellow-500 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-400",
                  entry.severity === 'info' && "bg-blue-50 border-blue-500 text-blue-700 dark:bg-blue-950 dark:text-blue-400"
                )}
              >
                <div className="flex items-start gap-2">
                  <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
                  <div className="flex-1">
                    <div className="font-medium">{entry.message}</div>
                    {entry.mailbox && (
                      <div className="text-muted-foreground mt-0.5">Mailbox: {entry.mailbox}</div>
                    )}
                    {entry.nextStep && (
                      <div className="text-muted-foreground mt-1 flex items-center gap-1">
                        <span>Next step:</span>
                        <span className="font-medium">{entry.nextStep}</span>
                      </div>
                    )}
                    <div className="text-muted-foreground mt-1">
                      {new Date(entry.timestamp).toLocaleString()}
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center gap-2">
          <span className="w-12 text-sm text-muted-foreground">Sub:</span>
          <Input
            className="flex-1 border-0 shadow-none focus-visible:ring-0 px-0 py-1 h-8"
            placeholder="Subject"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
          />
        </div>
      </div>

      {/* Formatting Toolbar */}
      <div className="flex items-center gap-1 border-b px-4 py-1 bg-muted/30">
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Bold (⌘B)">
          <Bold className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Italic (⌘I)">
          <Italic className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Underline (⌘U)">
          <Underline className="h-4 w-4" />
        </Button>
        <Separator orientation="vertical" className="h-6" />
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Insert link">
          <Link className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Bullet list">
          <List className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" className="h-8 w-8" title="Insert image">
          <Image className="h-4 w-4" />
        </Button>
        <span className="text-xs text-muted-foreground ml-2">
          Tip: Press ⌘+Enter to send
        </span>
      </div>

      {/* Body */}
      <div className="flex-1 overflow-hidden">
        <Textarea
          className="h-full resize-none border-0 shadow-none focus-visible:ring-0 p-4"
          placeholder="Write your message..."
          value={body}
          onChange={(e) => setBody(e.target.value)}
        />
      </div>

      {/* Attachments & Footer */}
      {attachments.length > 0 && (
        <div className="border-t px-4 py-2 bg-muted/30">
          <div className="flex flex-wrap gap-2">
            {attachments.map((att) => (
              <div
                key={att.id}
                className="flex items-center gap-2 rounded border bg-background px-3 py-1.5"
              >
                <Paperclip className="h-4 w-4 text-muted-foreground" />
                <span className="text-sm">{att.name}</span>
                <span className="text-xs text-muted-foreground">
                  ({formatSize(att.size)})
                </span>
                <button
                  onClick={() => removeAttachment(att.id)}
                  className="ml-1 rounded-full hover:bg-muted p-0.5"
                >
                  <X className="h-3 w-3" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="flex items-center justify-between border-t px-4 py-2">
        <div className="flex items-center gap-2">
          <Button variant="outline" size="icon" onClick={() => fileInputRef.current?.click()}>
            <Paperclip className="h-5 w-5" />
          </Button>
          <input
            type="file"
            multiple
            ref={fileInputRef}
            className="hidden"
            onChange={handleAttach}
          />
        </div>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <kbd className="rounded border px-1.5 py-0.5 text-xs bg-muted">⌘</kbd>
          <span>+</span>
          <kbd className="rounded border px-1.5 py-0.5 text-xs bg-muted">Enter</kbd>
          <span>to send</span>
        </div>
      </div>
    </div>
  )
}

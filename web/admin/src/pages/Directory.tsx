import { useState, useEffect } from "react";
import {
  FolderSearch,
  Plus,
  Search,
  MoreHorizontal,
  Edit,
  Trash2,
  RefreshCw,
  Users,
  Building,
  MapPin,
  CheckCircle,
  AlertCircle,
  List,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";
import { useDirectory } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { DirectoryObject } from "@/types";

export function Directory() {
  const { t } = useI18n();
  const {
    resources,
    bookingPolicies,
    roomLists,
    loading,
    fetchDirectory,
    createResource,
    updateResource,
    deleteResource,
    createRoomList,
    deleteRoomList,
  } = useDirectory();

  const [activeTab, setActiveTab] = useState("gal");
  const [searchQuery, setSearchQuery] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const [isAddResourceDialogOpen, setIsAddResourceDialogOpen] = useState(false);
  const [newResourceName, setNewResourceName] = useState("");
  const [newResourceType, setNewResourceType] = useState<"room" | "equipment">("room");
  const [newResourceCapacity, setNewResourceCapacity] = useState(10);

  const [editResourceTarget, setEditResourceTarget] = useState<DirectoryObject | null>(null);
  const [editResourceCapacity, setEditResourceCapacity] = useState(0);
  const [deleteResourceTarget, setDeleteResourceTarget] = useState<DirectoryObject | null>(null);

  // Per-policy max-duration text being edited; saved on blur.
  const [maxDurationDrafts, setMaxDurationDrafts] = useState<Record<string, string>>({});

  const [isAddRoomListDialogOpen, setIsAddRoomListDialogOpen] = useState(false);
  const [newRoomListName, setNewRoomListName] = useState("");
  const [newRoomListRooms, setNewRoomListRooms] = useState<string[]>([]);

  useEffect(() => {
    fetchDirectory().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchDirectory]);

  const handleAddResource = async () => {
    if (!newResourceName) return;
    const email = newResourceName.toLowerCase().replace(/\s+/g, "-") + "@local.test";
    try {
      await createResource({
        name: newResourceName,
        email,
        type: newResourceType,
        capacity: newResourceType === "room" ? newResourceCapacity : 0,
      });
      setFormError(null);
      setIsAddResourceDialogOpen(false);
      setNewResourceName("");
      setNewResourceType("room");
      setNewResourceCapacity(10);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.addFailed"));
    }
  };

  const handleToggleHidden = async (obj: DirectoryObject) => {
    try {
      await updateResource(obj.id, { isHidden: !obj.isHidden });
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.updateVisibilityFailed"));
    }
  };

  const handleToggleBookable = async (obj: DirectoryObject) => {
    try {
      await updateResource(obj.id, { isBookable: !obj.isBookable });
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.updateBookableFailed"));
    }
  };

  const handleDeleteResource = async () => {
    if (!deleteResourceTarget) return;
    try {
      await deleteResource(deleteResourceTarget.id);
      setDeleteResourceTarget(null);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.removeFailed"));
    }
  };

  // handleBookingUpdate applies a partial booking-policy change to the
  // underlying resource. The backend single-sources the booking decision, so
  // toggling auto-accept/requires-approval both write requiresApproval.
  const handleBookingUpdate = async (id: string, patch: { allowRecurring?: boolean; requiresApproval?: boolean; maxDuration?: number }) => {
    try {
      await updateResource(id, patch);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.updateBookingFailed"));
    }
  };

  const openEditResource = (obj: DirectoryObject) => {
    setEditResourceTarget(obj);
    setEditResourceCapacity(obj.capacity ?? 0);
    setFormError(null);
  };

  // The backend resource update accepts capacity (name and type are fixed at
  // creation); booking rules are managed on the Booking Policy tab.
  const handleEditResource = async () => {
    if (!editResourceTarget) return;
    try {
      await updateResource(editResourceTarget.id, { capacity: editResourceCapacity });
      setEditResourceTarget(null);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.updateFailed"));
    }
  };

  const handleAddRoomList = async () => {
    if (!newRoomListName) return;
    try {
      await createRoomList(newRoomListName, newRoomListRooms);
      setFormError(null);
      setIsAddRoomListDialogOpen(false);
      setNewRoomListName("");
      setNewRoomListRooms([]);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.createRoomListFailed"));
    }
  };

  const handleDeleteRoomList = async (id: string) => {
    try {
      await deleteRoomList(id);
      setFormError(null);
    } catch (err) {
      setFormError((err as { message?: string }).message || t("directory.deleteRoomListFailed"));
    }
  };

  const toggleRoomInNewList = (email: string) => {
    setNewRoomListRooms((prev) =>
      prev.includes(email) ? prev.filter((e) => e !== email) : [...prev, email]
    );
  };

  const rooms = resources.filter((obj) => obj.type === "room");

  const filteredObjects = resources.filter(
    (obj) =>
      obj.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      obj.email.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const getObjectIcon = (type: string) => {
    switch (type) {
      case "user":
        return <Users className="h-4 w-4" />;
      case "room":
        return <Building className="h-4 w-4" />;
      case "equipment":
        return <MapPin className="h-4 w-4" />;
      case "distribution-group":
        return <FolderSearch className="h-4 w-4" />;
      default:
        return <Users className="h-4 w-4" />;
    }
  };

  const objectTypeLabel = (type: string) => {
    switch (type) {
      case "room":
        return t("directory.typeRoom");
      case "equipment":
        return t("directory.typeEquipment");
      case "user":
        return t("directory.typeUser");
      case "distribution-group":
        return t("directory.typeDistributionGroup");
      default:
        return type;
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("directory.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("directory.description")}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => fetchDirectory().catch(() => {})} disabled={loading}>
            <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
            {t("common.refresh")}
          </Button>
          <Dialog open={isAddResourceDialogOpen} onOpenChange={setIsAddResourceDialogOpen}>
            {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
            <DialogTrigger asChild>
              <Button>
                <Plus className="mr-2 h-4 w-4" />
                {t("directory.addResource")}
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>{t("directory.addDialogTitle")}</DialogTitle>
                <DialogDescription>
                  {t("directory.addDialogDescription")}
                </DialogDescription>
              </DialogHeader>
              {formError && (
                <Alert variant="destructive">
                  <AlertCircle className="h-4 w-4" />
                  <AlertDescription>{formError}</AlertDescription>
                </Alert>
              )}
              <div className="space-y-4 py-4">
                <div className="space-y-2">
                  <Label htmlFor="resource-name">{t("directory.resourceName")}</Label>
                  <Input
                    id="resource-name"
                    placeholder={t("directory.resourceNamePlaceholder")}
                    value={newResourceName}
                    onChange={(e) => setNewResourceName(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="resource-type">{t("common.type")}</Label>
                  <select
                    id="resource-type"
                    className="w-full p-2 border rounded-md bg-background"
                    value={newResourceType}
                    onChange={(e) => setNewResourceType(e.target.value as "room" | "equipment")}
                  >
                    <option value="room">{t("directory.typeRoom")}</option>
                    <option value="equipment">{t("directory.typeEquipment")}</option>
                  </select>
                </div>
                {newResourceType === "room" && (
                  <div className="space-y-2">
                    <Label htmlFor="resource-capacity">{t("directory.capacity")}</Label>
                    <Input
                      id="resource-capacity"
                      type="number"
                      value={newResourceCapacity}
                      onChange={(e) => setNewResourceCapacity(parseInt(e.target.value) || 10)}
                    />
                  </div>
                )}
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={() => setIsAddResourceDialogOpen(false)}>
                  {t("common.cancel")}
                </Button>
                <Button onClick={handleAddResource} disabled={!newResourceName}>
                  {t("directory.addResource")}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {/* Edit Resource Dialog */}
      <Dialog open={editResourceTarget !== null} onOpenChange={(open) => { if (!open) setEditResourceTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("directory.editResourceTitle")}</DialogTitle>
            <DialogDescription>
              {t("directory.editResourceDescription", { name: editResourceTarget?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="edit-resource-capacity">{t("directory.capacity")}</Label>
              <Input
                id="edit-resource-capacity"
                type="number"
                min={0}
                value={editResourceCapacity}
                onChange={(e) => setEditResourceCapacity(Math.max(0, parseInt(e.target.value) || 0))}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditResourceTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button onClick={handleEditResource}>{t("common.saveChanges")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Remove Resource Confirmation Dialog */}
      <Dialog open={deleteResourceTarget !== null} onOpenChange={(open) => { if (!open) setDeleteResourceTarget(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("directory.removeResourceTitle")}</DialogTitle>
            <DialogDescription>
              {t("directory.removeResourceDescription", { name: deleteResourceTarget?.name ?? "" })}
            </DialogDescription>
          </DialogHeader>
          {formError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteResourceTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={handleDeleteResource}>
              {t("common.remove")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {formError && !isAddResourceDialogOpen && !isAddRoomListDialogOpen && editResourceTarget === null && deleteResourceTarget === null && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="gal">
            <Users className="h-4 w-4 mr-2" />
            {t("directory.globalAddressList")}
          </TabsTrigger>
          <TabsTrigger value="rooms">
            <Building className="h-4 w-4 mr-2" />
            {t("directory.rooms")}
          </TabsTrigger>
          <TabsTrigger value="room-lists">
            <List className="h-4 w-4 mr-2" />
            {t("directory.roomLists")}
          </TabsTrigger>
          <TabsTrigger value="booking">
            <CheckCircle className="h-4 w-4 mr-2" />
            {t("directory.bookingPolicy")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="gal" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("directory.globalAddressList")}</CardTitle>
              <CardDescription>
                {t("directory.galDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="mb-4">
                <div className="relative max-w-sm">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    placeholder={t("directory.searchPlaceholder")}
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    className="pl-10"
                  />
                </div>
              </div>

              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : filteredObjects.length === 0 ? (
                <div className="text-center py-8">
                  <Users className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("directory.emptyGalTitle")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("directory.emptyGalDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {filteredObjects.map((obj) => (
                    <div
                      key={obj.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className={cn("p-2 rounded-lg",
                          obj.type === "room" ? "bg-blue-500/10" :
                          obj.type === "equipment" ? "bg-violet-500/10" : "bg-muted"
                        )}>
                          {getObjectIcon(obj.type)}
                        </div>
                        <div>
                          <div className="font-medium">{obj.name}</div>
                          <div className="text-sm text-muted-foreground">{obj.email}</div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="secondary" className="text-xs capitalize">{objectTypeLabel(obj.type)}</Badge>
                        {obj.capacity ? (
                          <Badge variant="outline" className="text-xs">{t("directory.seatsCount", { count: String(obj.capacity) })}</Badge>
                        ) : null}
                        <Switch checked={!obj.isHidden} onCheckedChange={() => handleToggleHidden(obj)} />
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onClick={() => openEditResource(obj)}>
                              <Edit className="mr-2 h-4 w-4" />
                              {t("common.edit")}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              className="text-red-600"
                              onClick={() => setDeleteResourceTarget(obj)}
                            >
                              <Trash2 className="mr-2 h-4 w-4" />
                              {t("common.remove")}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="rooms" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("directory.roomResources")}</CardTitle>
              <CardDescription>
                {t("directory.roomResourcesDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : (
                <div className="space-y-4">
                  {rooms.map((room) => (
                    <div
                      key={room.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-blue-500/10">
                          <Building className="h-4 w-4 text-blue-500" />
                        </div>
                        <div>
                          <div className="font-medium">{room.name}</div>
                          <div className="text-sm text-muted-foreground">
                            {room.capacity ? `${t("directory.seatsCount", { count: String(room.capacity) })} | ` : ""}{room.email}
                          </div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Switch checked={room.isBookable} onCheckedChange={() => handleToggleBookable(room)} />
                        {room.isBookable ? (
                          <Badge variant="default" className="bg-emerald-500">{t("directory.bookable")}</Badge>
                        ) : (
                          <Badge variant="secondary">{t("directory.notBookable")}</Badge>
                        )}
                      </div>
                    </div>
                  ))}

                  {rooms.length === 0 && (
                    <div className="text-center py-8">
                      <Building className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                      <h3 className="text-lg font-medium">{t("directory.emptyRoomsTitle")}</h3>
                      <p className="text-muted-foreground mt-1">
                        {t("directory.emptyRoomsDescription")}
                      </p>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="room-lists" className="space-y-4">
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between">
                <div>
                  <CardTitle>{t("directory.roomLists")}</CardTitle>
                  <CardDescription>
                    {t("directory.roomListsDescription")}
                  </CardDescription>
                </div>
                <Dialog open={isAddRoomListDialogOpen} onOpenChange={setIsAddRoomListDialogOpen}>
                  {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                  <DialogTrigger asChild>
                    <Button size="sm">
                      <Plus className="mr-2 h-4 w-4" />
                      {t("directory.addRoomList")}
                    </Button>
                  </DialogTrigger>
                  <DialogContent className="sm:max-w-md">
                    <DialogHeader>
                      <DialogTitle>{t("directory.addRoomList")}</DialogTitle>
                      <DialogDescription>
                        {t("directory.addRoomListDescription")}
                      </DialogDescription>
                    </DialogHeader>
                    {formError && (
                      <Alert variant="destructive">
                        <AlertCircle className="h-4 w-4" />
                        <AlertDescription>{formError}</AlertDescription>
                      </Alert>
                    )}
                    <div className="space-y-4 py-4">
                      <div className="space-y-2">
                        <Label htmlFor="room-list-name">{t("directory.listName")}</Label>
                        <Input
                          id="room-list-name"
                          placeholder={t("directory.listNamePlaceholder")}
                          value={newRoomListName}
                          onChange={(e) => setNewRoomListName(e.target.value)}
                        />
                      </div>
                      <div className="space-y-2">
                        <Label>{t("directory.rooms")}</Label>
                        {rooms.length === 0 ? (
                          <p className="text-sm text-muted-foreground">
                            {t("directory.noRoomsAvailable")}
                          </p>
                        ) : (
                          <div className="space-y-2 max-h-48 overflow-y-auto rounded-md border p-2">
                            {rooms.map((room) => (
                              <label
                                key={room.id}
                                className="flex items-center gap-2 text-sm cursor-pointer"
                              >
                                <input
                                  type="checkbox"
                                  checked={newRoomListRooms.includes(room.email)}
                                  onChange={() => toggleRoomInNewList(room.email)}
                                />
                                {room.name} <span className="text-muted-foreground">({room.email})</span>
                              </label>
                            ))}
                          </div>
                        )}
                      </div>
                    </div>
                    <DialogFooter>
                      <Button variant="outline" onClick={() => setIsAddRoomListDialogOpen(false)}>
                        {t("common.cancel")}
                      </Button>
                      <Button onClick={handleAddRoomList} disabled={!newRoomListName}>
                        {t("directory.createList")}
                      </Button>
                    </DialogFooter>
                  </DialogContent>
                </Dialog>
              </div>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-16 w-full" />
                </div>
              ) : roomLists.length === 0 ? (
                <div className="text-center py-8">
                  <List className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("directory.emptyRoomListsTitle")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("directory.emptyRoomListsDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {roomLists.map((list) => (
                    <div
                      key={list.id}
                      className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center gap-3">
                        <div className="p-2 rounded-lg bg-blue-500/10">
                          <List className="h-4 w-4 text-blue-500" />
                        </div>
                        <div>
                          <div className="font-medium">{list.name}</div>
                          <div className="text-sm text-muted-foreground">
                            {list.rooms.length === 1
                              ? t("directory.roomCountSingular", { count: String(list.rooms.length) })
                              : t("directory.roomCountPlural", { count: String(list.rooms.length) })}
                            {list.rooms.length > 0 ? `: ${list.rooms.join(", ")}` : ""}
                          </div>
                        </div>
                      </div>
                      <Button variant="ghost" size="sm" onClick={() => handleDeleteRoomList(list.id)}>
                        <Trash2 className="h-4 w-4 text-red-500" />
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="booking" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>{t("directory.bookingPolicyTitle")}</CardTitle>
              <CardDescription>
                {t("directory.bookingPolicyDescription")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {loading ? (
                <div className="space-y-4">
                  <Skeleton className="h-20 w-full" />
                  <Skeleton className="h-20 w-full" />
                </div>
              ) : bookingPolicies.length === 0 ? (
                <div className="text-center py-8">
                  <CheckCircle className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                  <h3 className="text-lg font-medium">{t("directory.emptyBookingTitle")}</h3>
                  <p className="text-muted-foreground mt-1">
                    {t("directory.emptyBookingDescription")}
                  </p>
                </div>
              ) : (
                <div className="space-y-3">
                  {bookingPolicies.map((policy) => (
                    <div
                      key={policy.id}
                      className="p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                    >
                      <div className="flex items-center justify-between mb-4">
                        <div className="flex items-center gap-3">
                          <div className="p-2 rounded-lg bg-blue-500/10">
                            <Building className="h-4 w-4 text-blue-500" />
                          </div>
                          <div>
                            <div className="font-medium">{policy.resourceName}</div>
                            <div className="text-sm text-muted-foreground">
                              {t("directory.maxDurationMinutes", { count: String(policy.maxDuration) })}
                            </div>
                          </div>
                        </div>
                      </div>
                      <div className="grid gap-4 md:grid-cols-4">
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">{t("directory.autoAccept")}</Label>
                          {/* Auto-accept is the inverse of requires-approval. */}
                          <Switch
                            checked={policy.autoAccept}
                            onCheckedChange={(v) =>
                              handleBookingUpdate(policy.id, { requiresApproval: !v })
                            }
                          />
                        </div>
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">{t("directory.allowRecurring")}</Label>
                          <Switch
                            checked={policy.allowRecurring}
                            onCheckedChange={(v) =>
                              handleBookingUpdate(policy.id, { allowRecurring: v })
                            }
                          />
                        </div>
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">{t("directory.requiresApproval")}</Label>
                          <Switch
                            checked={policy.requiresApproval}
                            onCheckedChange={(v) =>
                              handleBookingUpdate(policy.id, { requiresApproval: v })
                            }
                          />
                        </div>
                        <div className="flex items-center justify-between gap-2 p-3 rounded-lg bg-muted">
                          <Label className="text-xs" htmlFor={`maxdur-${policy.id}`}>
                            {t("directory.maxDurationLabel")}
                          </Label>
                          <Input
                            id={`maxdur-${policy.id}`}
                            type="number"
                            min={0}
                            className="h-8 w-20"
                            value={maxDurationDrafts[policy.id] ?? String(policy.maxDuration)}
                            onChange={(e) =>
                              setMaxDurationDrafts((d) => ({ ...d, [policy.id]: e.target.value }))
                            }
                            onBlur={() => {
                              const raw = maxDurationDrafts[policy.id];
                              if (raw === undefined) return;
                              const next = Math.max(0, parseInt(raw, 10) || 0);
                              setMaxDurationDrafts((d) => {
                                const { [policy.id]: _drop, ...rest } = d;
                                void _drop;
                                return rest;
                              });
                              if (next !== policy.maxDuration) {
                                handleBookingUpdate(policy.id, { maxDuration: next });
                              }
                            }}
                          />
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}

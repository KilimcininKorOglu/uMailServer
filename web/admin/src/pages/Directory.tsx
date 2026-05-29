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
  XCircle,
  AlertCircle,
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
import { cn } from "@/lib/utils";

interface DirectoryObject {
  id: string;
  name: string;
  email: string;
  type: "user" | "room" | "equipment" | "distribution-group";
  isHidden: boolean;
  isBookable: boolean;
  capacity?: number;
}

interface RoomList {
  id: string;
  name: string;
  rooms: string[];
}

interface BookingPolicy {
  id: string;
  resourceName: string;
  autoAccept: boolean;
  allowRecurring: boolean;
  maxDuration: number;
  requiresApproval: boolean;
  approvalDelegate: string;
}

export function Directory() {
  const [activeTab, setActiveTab] = useState("gal");
  const [searchQuery, setSearchQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [directoryObjects, setDirectoryObjects] = useState<DirectoryObject[]>([]);
  const [_roomLists, setRoomLists] = useState<RoomList[]>([]);
  const [bookingPolicies, setBookingPolicies] = useState<BookingPolicy[]>([]);
  const [isAddResourceDialogOpen, setIsAddResourceDialogOpen] = useState(false);
  const [newResourceName, setNewResourceName] = useState("");
  const [newResourceType, setNewResourceType] = useState<"room" | "equipment">("room");
  const [newResourceCapacity, setNewResourceCapacity] = useState(10);

  useEffect(() => {
    fetchDirectoryObjects();
    fetchRoomLists();
    fetchBookingPolicies();
  }, []);

  const fetchDirectoryObjects = async () => {
    setLoading(true);
    // Placeholder - would fetch from /api/v1/admin/directory
    setDirectoryObjects([
      { id: "1", name: "Admin User", email: "admin@local.test", type: "user", isHidden: false, isBookable: false },
      { id: "2", name: "Conference Room A", email: "conf-a@local.test", type: "room", isHidden: false, isBookable: true, capacity: 10 },
      { id: "3", name: "Projector Equipment", email: "projector@local.test", type: "equipment", isHidden: false, isBookable: true },
    ]);
    setLoading(false);
  };

  const fetchRoomLists = async () => {
    setRoomLists([
      { id: "rl-1", name: "Room List", rooms: ["conf-a@local.test"] },
    ]);
  };

  const fetchBookingPolicies = async () => {
    setBookingPolicies([
      {
        id: "bp-1",
        resourceName: "Conference Room A",
        autoAccept: true,
        allowRecurring: true,
        maxDuration: 480,
        requiresApproval: false,
        approvalDelegate: "",
      },
    ]);
  };

  const handleAddResource = async () => {
    if (!newResourceName) return;

    const newObject: DirectoryObject = {
      id: Date.now().toString(),
      name: newResourceName,
      email: newResourceName.toLowerCase().replace(/\s+/g, "-") + "@local.test",
      type: newResourceType,
      isHidden: false,
      isBookable: true,
      capacity: newResourceType === "room" ? newResourceCapacity : undefined,
    };

    setDirectoryObjects((prev) => [...prev, newObject]);
    setIsAddResourceDialogOpen(false);
    setNewResourceName("");
    setNewResourceType("room");
    setNewResourceCapacity(10);
  };

  const handleToggleHidden = (obj: DirectoryObject) => {
    setDirectoryObjects((prev) =>
      prev.map((o) => (o.id === obj.id ? { ...o, isHidden: !o.isHidden } : o))
    );
  };

  const handleToggleBookable = (obj: DirectoryObject) => {
    setDirectoryObjects((prev) =>
      prev.map((o) => (o.id === obj.id ? { ...o, isBookable: !o.isBookable } : o))
    );
  };

  const handleDeleteResource = (id: string) => {
    setDirectoryObjects((prev) => prev.filter((o) => o.id !== id));
  };

  const filteredObjects = directoryObjects.filter(
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

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Directory</h1>
          <p className="text-muted-foreground mt-1">
            Manage GAL visibility, rooms, resources, and booking policy
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => { fetchDirectoryObjects(); fetchRoomLists(); fetchBookingPolicies(); }} disabled={loading}>
            <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
            Refresh
          </Button>
          <Dialog open={isAddResourceDialogOpen} onOpenChange={setIsAddResourceDialogOpen}>
            {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
            <DialogTrigger asChild>
              <Button>
                <Plus className="mr-2 h-4 w-4" />
                Add Resource
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>Add Directory Resource</DialogTitle>
                <DialogDescription>
                  Add a room or equipment resource to the directory
                </DialogDescription>
              </DialogHeader>
              <div className="space-y-4 py-4">
                <div className="space-y-2">
                  <Label htmlFor="resource-name">Resource Name</Label>
                  <Input
                    id="resource-name"
                    placeholder="Conference Room A"
                    value={newResourceName}
                    onChange={(e) => setNewResourceName(e.target.value)}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="resource-type">Type</Label>
                  <select
                    id="resource-type"
                    className="w-full p-2 border rounded-md bg-background"
                    value={newResourceType}
                    onChange={(e) => setNewResourceType(e.target.value as "room" | "equipment")}
                  >
                    <option value="room">Room</option>
                    <option value="equipment">Equipment</option>
                  </select>
                </div>
                {newResourceType === "room" && (
                  <div className="space-y-2">
                    <Label htmlFor="resource-capacity">Capacity</Label>
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
                  Cancel
                </Button>
                <Button onClick={handleAddResource} disabled={!newResourceName}>
                  Add Resource
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList>
          <TabsTrigger value="gal">
            <Users className="h-4 w-4 mr-2" />
            Global Address List
          </TabsTrigger>
          <TabsTrigger value="rooms">
            <Building className="h-4 w-4 mr-2" />
            Rooms
          </TabsTrigger>
          <TabsTrigger value="booking">
            <CheckCircle className="h-4 w-4 mr-2" />
            Booking Policy
          </TabsTrigger>
        </TabsList>

        <TabsContent value="gal" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Global Address List</CardTitle>
              <CardDescription>
                All directory objects visible in GAL lookup
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="mb-4">
                <div className="relative max-w-sm">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    placeholder="Search directory..."
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
                  <h3 className="text-lg font-medium">No directory objects</h3>
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
                        <Badge variant="secondary" className="text-xs capitalize">{obj.type}</Badge>
                        {obj.capacity && (
                          <Badge variant="outline" className="text-xs">{obj.capacity} seats</Badge>
                        )}
                        <Switch checked={!obj.isHidden} onCheckedChange={() => handleToggleHidden(obj)} />
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem>
                              <Edit className="mr-2 h-4 w-4" />
                              Edit
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              className="text-red-600"
                              onClick={() => handleDeleteResource(obj.id)}
                            >
                              <Trash2 className="mr-2 h-4 w-4" />
                              Remove
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
              <CardTitle>Room Resources</CardTitle>
              <CardDescription>
                Bookable room resources and room lists
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
                  {directoryObjects
                    .filter((obj) => obj.type === "room")
                    .map((room) => (
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
                              {room.capacity} seats | {room.email}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          <Switch checked={room.isBookable} onCheckedChange={() => handleToggleBookable(room)} />
                          {room.isBookable ? (
                            <Badge variant="default" className="bg-emerald-500">Bookable</Badge>
                          ) : (
                            <Badge variant="secondary">Not bookable</Badge>
                          )}
                        </div>
                      </div>
                    ))}

                  {directoryObjects.filter((obj) => obj.type === "room").length === 0 && (
                    <div className="text-center py-8">
                      <Building className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
                      <h3 className="text-lg font-medium">No room resources</h3>
                      <p className="text-muted-foreground mt-1">
                        Add room resources to make them bookable
                      </p>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="booking" className="space-y-4">
          <Card>
            <CardHeader>
              <CardTitle>Resource Booking Policy</CardTitle>
              <CardDescription>
                Configure auto-accept, recurring, and approval settings per resource
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
                  <h3 className="text-lg font-medium">No booking policies</h3>
                  <p className="text-muted-foreground mt-1">
                    Resource booking policies will appear here
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
                              Max duration: {policy.maxDuration} minutes
                            </div>
                          </div>
                        </div>
                        <DropdownMenu>
                          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="icon" className="h-8 w-8">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem>
                              <Edit className="mr-2 h-4 w-4" />
                              Edit Policy
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                      <div className="grid gap-4 md:grid-cols-4">
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">Auto-Accept</Label>
                          {policy.autoAccept ? (
                            <CheckCircle className="h-4 w-4 text-emerald-500" />
                          ) : (
                            <XCircle className="h-4 w-4 text-red-500" />
                          )}
                        </div>
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">Allow Recurring</Label>
                          {policy.allowRecurring ? (
                            <CheckCircle className="h-4 w-4 text-emerald-500" />
                          ) : (
                            <XCircle className="h-4 w-4 text-red-500" />
                          )}
                        </div>
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">Requires Approval</Label>
                          {policy.requiresApproval ? (
                            <AlertCircle className="h-4 w-4 text-amber-500" />
                          ) : (
                            <CheckCircle className="h-4 w-4 text-emerald-500" />
                          )}
                        </div>
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted">
                          <Label className="text-xs">Max Duration</Label>
                          <span className="text-sm font-medium">{policy.maxDuration}m</span>
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

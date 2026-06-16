import { Link, useLocation } from "react-router-dom";
import {
  LayoutDashboard,
  Globe,
  Users,
  AtSign,
  Mail,
  Settings,
  Server,
  Shield,
  UsersRound,
  ActivitySquare,
  FolderSearch,
  FolderLock,
  Briefcase,
  Building2,
  ChevronLeft,
  ChevronRight,
  LogOut,
  Network,
  ShieldCheck,
  ScrollText,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/hooks/useI18n";
import LanguageSelector from "@/components/LanguageSelector";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { User } from "@/types";

interface SidebarProps {
  isCollapsed: boolean;
  onToggle: () => void;
  user: User | null;
  onLogout: () => void;
}

const menuItems = [
  { path: "/", icon: LayoutDashboard, labelKey: "nav.dashboard" },
  { path: "/domains", icon: Globe, labelKey: "nav.domains" },
  { path: "/public-folders", icon: FolderLock, labelKey: "nav.publicFolders" },
  { path: "/accounts", icon: Users, labelKey: "nav.accounts" },
  { path: "/aliases", icon: AtSign, labelKey: "nav.aliases" },
  { path: "/groups", icon: UsersRound, labelKey: "nav.groups" },
  { path: "/queue", icon: Mail, labelKey: "nav.queue" },
  { path: "/policies", icon: Shield, labelKey: "nav.policies" },
  { path: "/delegation", icon: UsersRound, labelKey: "nav.delegation" },
  { path: "/diagnostics", icon: ActivitySquare, labelKey: "nav.diagnostics" },
  { path: "/logs", icon: ScrollText, labelKey: "nav.logs" },
  { path: "/directory", icon: FolderSearch, labelKey: "nav.directory" },
  { path: "/jobs", icon: Briefcase, labelKey: "nav.jobs" },
  { path: "/tenants", icon: Building2, labelKey: "nav.tenants" },
  { path: "/cluster", icon: Network, labelKey: "nav.cluster" },
  { path: "/certificates", icon: ShieldCheck, labelKey: "nav.certificates" },
  { path: "/settings", icon: Settings, labelKey: "nav.settings" },
];

export function Sidebar({ isCollapsed, onToggle, user, onLogout }: SidebarProps) {
  const location = useLocation();
  const { t } = useI18n();

  return (
    <TooltipProvider delay={0}>
      <aside
        className={cn(
          "fixed left-0 top-0 z-40 h-screen bg-card border-r border-border transition-all duration-300 ease-in-out",
          isCollapsed ? "w-16" : "w-64"
        )}
      >
        {/* Header */}
        <div className="flex h-16 items-center justify-between px-4 border-b border-border">
          <div className="flex items-center gap-2 overflow-hidden">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary flex-shrink-0">
              <Server className="h-4 w-4 text-primary-foreground" />
            </div>
            {!isCollapsed && (
              <span className="font-semibold text-lg truncate">uMail Admin</span>
            )}
          </div>
          <Button
            variant="ghost"
            size="icon"
            onClick={onToggle}
            className="h-8 w-8 flex-shrink-0"
          >
            {isCollapsed ? (
              <ChevronRight className="h-4 w-4" />
            ) : (
              <ChevronLeft className="h-4 w-4" />
            )}
          </Button>
        </div>

        {/* Navigation */}
        <nav className="flex-1 p-2 space-y-1">
          {menuItems.map((item) => {
            const isActive = location.pathname === item.path;
            const label = t(item.labelKey);
            const content = (
              <Link
                to={item.path}
                className={cn(
                  "flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
                  isActive
                    ? "bg-primary/10 text-primary"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
                )}
              >
                <item.icon className={cn("h-5 w-5 flex-shrink-0", isActive && "text-primary")} />
                {!isCollapsed && <span className="truncate">{label}</span>}
              </Link>
            );

            if (isCollapsed) {
              return (
                <Tooltip key={item.path}>
                  {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
                  <TooltipTrigger asChild>{content}</TooltipTrigger>
                  <TooltipContent side="right">{label}</TooltipContent>
                </Tooltip>
              );
            }

            return <div key={item.path}>{content}</div>;
          })}
        </nav>

        {/* Footer */}
        <div className="absolute bottom-0 left-0 right-0 p-2 border-t border-border">
          {!isCollapsed && (
            <div className="px-3 py-2">
              <LanguageSelector />
            </div>
          )}
          <div className={cn("flex items-center gap-3 px-3 py-2", isCollapsed && "justify-center")}>
            {!isCollapsed && (
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium truncate">{user?.email}</p>
                <p className="text-xs text-muted-foreground">Administrator</p>
              </div>
            )}
            <Tooltip>
              {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={onLogout}
                  className="h-8 w-8 text-muted-foreground hover:text-destructive"
                >
                  <LogOut className="h-4 w-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent side={isCollapsed ? "right" : "top"}>{t("nav.logout")}</TooltipContent>
            </Tooltip>
          </div>
        </div>
      </aside>
    </TooltipProvider>
  );
}

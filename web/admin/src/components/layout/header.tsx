import { useLocation } from "react-router-dom";
import { Sun, Moon, Monitor } from "lucide-react";
import { useTheme } from "@/components/theme-provider";
import { useI18n } from "@/hooks/useI18n";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

interface HeaderProps {
  isConnected: boolean;
}

// Each route maps to a nav translation key so the page title follows the
// selected language (the sidebar uses the same nav.* keys).
const routeNavKey: Record<string, string> = {
  "/": "dashboard",
  "/domains": "domains",
  "/accounts": "accounts",
  "/aliases": "aliases",
  "/groups": "groups",
  "/queue": "queue",
  "/policies": "policies",
  "/delegation": "delegation",
  "/diagnostics": "diagnostics",
  "/directory": "directory",
  "/jobs": "jobs",
  "/tenants": "tenants",
  "/cluster": "cluster",
  "/certificates": "certificates",
  "/settings": "settings",
};

export function Header({ isConnected }: HeaderProps) {
  const { setTheme, resolvedTheme } = useTheme();
  const { t } = useI18n();
  const location = useLocation();

  const pageTitle = t(`nav.${routeNavKey[location.pathname] || "dashboard"}`);

  return (
    <header className="sticky top-0 z-30 h-16 bg-card border-b border-border px-6 flex items-center justify-between">
      {/* Left side - Breadcrumb */}
      <div className="flex items-center gap-4">
        <h1 className="text-xl font-semibold">{pageTitle}</h1>
      </div>

      {/* Right side - Actions */}
      <div className="flex items-center gap-2">
        {/* Realtime Status */}
        <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-muted mr-2">
          <div
            className={cn(
              "w-2 h-2 rounded-full animate-pulse",
              isConnected ? "bg-green-500" : "bg-red-500"
            )}
          />
          <span className="text-xs font-medium text-muted-foreground">
            {isConnected ? t("common.live") : t("common.offline")}
          </span>
        </div>

        {/* Theme Toggle */}
        <DropdownMenu>
          {/* @ts-expect-error asChild prop not typed in Base UI but works at runtime */}
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-9 w-9">
              {resolvedTheme === "dark" ? (
                <Moon className="h-4 w-4" />
              ) : (
                <Sun className="h-4 w-4" />
              )}
              <span className="sr-only">{t("common.toggleTheme")}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => setTheme("light")}>
              <Sun className="mr-2 h-4 w-4" />
              {t("common.themeLight")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setTheme("dark")}>
              <Moon className="mr-2 h-4 w-4" />
              {t("common.themeDark")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => setTheme("system")}>
              <Monitor className="mr-2 h-4 w-4" />
              {t("common.themeSystem")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}

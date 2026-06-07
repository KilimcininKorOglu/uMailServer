import { useEffect } from "react";
import {
  Mail,
  Users,
  Globe,
  Server,
  Activity,
  CheckCircle,
  AlertTriangle,
  XCircle,
  Clock,
  RefreshCw,
} from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { useStats, useHealth, useMetrics } from "@/hooks/useApi";
import { useI18n } from "@/hooks/useI18n";
import type { Activity as ActivityType, ServiceStatus } from "@/types";

type TranslateFn = (key: string, params?: Record<string, string>) => string;

interface DashboardProps {
  isConnected: boolean;
  activities: ActivityType[];
}

// Map a /health subsystem string ("ok" / "unavailable" / ...) to a card status.
function subsystemStatus(value?: string): ServiceStatus["status"] {
  if (value === "ok") return "operational";
  if (value === undefined) return "degraded";
  return "down";
}

export function Dashboard({ isConnected, activities }: DashboardProps) {
  const { t } = useI18n();
  const { stats, loading, fetchStats } = useStats();
  const { health, fetchHealth } = useHealth();
  const { metrics, fetchMetrics } = useMetrics();

  useEffect(() => {
    fetchStats();
    fetchHealth();
    fetchMetrics();
    const interval = setInterval(() => {
      fetchStats();
      fetchHealth();
      fetchMetrics();
    }, 30000);
    return () => clearInterval(interval);
  }, [fetchStats, fetchHealth, fetchMetrics]);

  // Derive the service cards from the real /health response instead of a static
  // always-"operational" array. If the health request failed entirely, health
  // is null and every subsystem reads as down.
  const serviceStatuses: ServiceStatus[] = [
    { name: t("dashboard.database"), status: health ? subsystemStatus(health.database) : "down" },
    { name: t("dashboard.queue"), status: health ? subsystemStatus(health.queue) : "down" },
    { name: t("dashboard.storage"), status: health ? subsystemStatus(health.storage) : "down" },
  ];

  const statCards = [
    {
      title: t("dashboard.totalDomains"),
      value: stats?.domains || 0,
      icon: Globe,
      color: "from-blue-500 to-blue-600",
      description: t("dashboard.activeEmailDomains"),
    },
    {
      title: t("dashboard.totalAccounts"),
      value: stats?.accounts || 0,
      icon: Users,
      color: "from-emerald-500 to-emerald-600",
      description: t("dashboard.registeredUsers"),
    },
    {
      title: t("dashboard.messagesToday"),
      value: stats?.messages || 0,
      icon: Mail,
      color: "from-violet-500 to-violet-600",
      description: t("dashboard.processedMessages"),
    },
    {
      title: t("dashboard.queueSize"),
      value: stats?.queue_size || 0,
      icon: Server,
      color: "from-orange-500 to-orange-600",
      description: t("dashboard.pendingEmails"),
    },
  ];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("dashboard.title")}</h1>
          <p className="text-muted-foreground mt-1">
            {t("dashboard.overviewSubtitle")}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => fetchStats()}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          {t("common.refresh")}
        </Button>
      </div>

      {/* Stats Grid */}
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {statCards.map((card, index) => (
          <StatCard key={index} {...card} loading={loading} />
        ))}
      </div>

      {/* Main Content Grid */}
      <div className="grid gap-6 lg:grid-cols-7">
        {/* System Status */}
        <Card className="lg:col-span-4">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Activity className="h-5 w-5" />
              {t("dashboard.systemStatus")}
            </CardTitle>
            <CardDescription>
              {t("dashboard.systemStatusSubtitle")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-4 sm:grid-cols-3">
              {serviceStatuses.map((service) => (
                <ServiceCard key={service.name} {...service} t={t} />
              ))}
            </div>

            {metrics && (
              <>
                <Separator className="my-6" />
                <div className="space-y-4">
                  <h4 className="text-sm font-medium">{t("dashboard.serverMetrics")}</h4>
                  <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                    <MetricStat
                      label={t("dashboard.smtpConnections")}
                      value={metrics.smtp?.connections ?? 0}
                    />
                    <MetricStat
                      label={t("dashboard.imapConnections")}
                      value={metrics.imap?.connections ?? 0}
                    />
                    <MetricStat
                      label={t("dashboard.messagesReceived")}
                      value={metrics.smtp?.messages ?? 0}
                    />
                    <MetricStat
                      label={t("dashboard.deliveries")}
                      value={metrics.delivery?.success ?? 0}
                      sub={t("dashboard.failedCount", { count: String(metrics.delivery?.failed ?? 0) })}
                    />
                  </div>
                </div>
              </>
            )}
          </CardContent>
        </Card>

        {/* Recent Activity */}
        <Card className="lg:col-span-3">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Clock className="h-5 w-5" />
              {t("dashboard.recentActivity")}
              {isConnected && (
                <Badge variant="secondary" className="text-xs">
                  {t("common.live")}
                </Badge>
              )}
            </CardTitle>
            <CardDescription>{t("dashboard.recentActivitySubtitle")}</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              {activities.length === 0 ? (
                <div className="text-center py-8 text-muted-foreground">
                  <Activity className="h-8 w-8 mx-auto mb-2 opacity-50" />
                  <p>{t("dashboard.noRecentActivity")}</p>
                </div>
              ) : (
                activities.slice(0, 10).map((activity) => (
                  <ActivityItem key={activity.id} {...activity} />
                ))
              )}
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

interface StatCardProps {
  title: string;
  value: number;
  icon: React.ElementType;
  color: string;
  description: string;
  loading?: boolean;
}

function StatCard({ title, value, icon: Icon, color, description, loading }: StatCardProps) {
  if (loading) {
    return (
      <Card>
        <CardContent className="p-6">
          <Skeleton className="h-12 w-12 rounded-lg mb-4" />
          <Skeleton className="h-8 w-24 mb-2" />
          <Skeleton className="h-4 w-32" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="relative overflow-hidden group hover:shadow-lg transition-shadow">
      <div className={cn("absolute top-0 right-0 w-32 h-32 bg-gradient-to-br opacity-10 rounded-bl-full", color)} />
      <CardContent className="p-6">
        <div className="flex items-start justify-between">
          <div className={cn("p-3 rounded-xl bg-gradient-to-br", color)}>
            <Icon className="h-6 w-6 text-white" />
          </div>
        </div>
        <div className="mt-4">
          <div className="text-3xl font-bold">{value.toLocaleString()}</div>
          <div className="text-sm font-medium text-muted-foreground">{title}</div>
          <div className="text-xs text-muted-foreground mt-1">{description}</div>
        </div>
      </CardContent>
    </Card>
  );
}

function ServiceCard({ name, status, port, t }: ServiceStatus & { t: TranslateFn }) {
  const statusConfig = {
    operational: { icon: CheckCircle, color: "text-emerald-500", bg: "bg-emerald-500/10" },
    degraded: { icon: AlertTriangle, color: "text-amber-500", bg: "bg-amber-500/10" },
    down: { icon: XCircle, color: "text-red-500", bg: "bg-red-500/10" },
  };

  const statusLabels = {
    operational: t("dashboard.statusOperational"),
    degraded: t("dashboard.statusDegraded"),
    down: t("dashboard.statusDown"),
  };

  const config = statusConfig[status];
  const Icon = config.icon;

  return (
    <div className="flex flex-col gap-3 p-4 rounded-lg bg-muted/50">
      <div className="flex items-center gap-2 min-w-0">
        <div className={cn("p-2 rounded-lg shrink-0", config.bg)}>
          <Icon className={cn("h-5 w-5", config.color)} />
        </div>
        <div className="min-w-0">
          <div className="font-medium truncate">{name}</div>
          {port !== undefined && (
            <div className="text-xs text-muted-foreground">{t("dashboard.port", { port: String(port) })}</div>
          )}
        </div>
      </div>
      <Badge
        variant={status === "operational" ? "default" : "secondary"}
        className={cn(
          "w-fit text-xs",
          status === "operational" && "bg-emerald-500 hover:bg-emerald-600"
        )}
      >
        {statusLabels[status]}
      </Badge>
    </div>
  );
}

interface MetricStatProps {
  label: string;
  value: number;
  sub?: string;
}

function MetricStat({ label, value, sub }: MetricStatProps) {
  return (
    <div className="rounded-lg border p-4">
      <div className="text-2xl font-bold">{value.toLocaleString()}</div>
      <div className="text-sm font-medium text-muted-foreground">{label}</div>
      {sub && <div className="text-xs text-muted-foreground mt-0.5">{sub}</div>}
    </div>
  );
}

function ActivityItem({ message, details, timestamp, severity = "info" }: ActivityType) {
  const severityConfig = {
    info: { icon: Activity, color: "text-blue-500", bg: "bg-blue-500/10" },
    success: { icon: CheckCircle, color: "text-emerald-500", bg: "bg-emerald-500/10" },
    warning: { icon: AlertTriangle, color: "text-amber-500", bg: "bg-amber-500/10" },
    error: { icon: XCircle, color: "text-red-500", bg: "bg-red-500/10" },
  };

  const config = severityConfig[severity];
  const Icon = config.icon;

  return (
    <div className="flex items-start gap-3 p-3 rounded-lg hover:bg-muted/50 transition-colors">
      <div className={cn("p-2 rounded-lg flex-shrink-0", config.bg)}>
        <Icon className={cn("h-4 w-4", config.color)} />
      </div>
      <div className="flex-1 min-w-0">
        <p className="text-sm font-medium">{message}</p>
        {details && <p className="text-xs text-muted-foreground mt-0.5">{details}</p>}
        <p className="text-xs text-muted-foreground mt-1">
          {new Date(timestamp).toLocaleTimeString()}
        </p>
      </div>
    </div>
  );
}

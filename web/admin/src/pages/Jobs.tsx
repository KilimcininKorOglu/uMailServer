import { useEffect } from "react";
import {
  Briefcase,
  RefreshCw,
  Database,
  Upload,
  Download,
  Clock,
  CheckCircle,
  XCircle,
  AlertCircle,
  Loader2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { useJobs } from "@/hooks/useApi";

export function Jobs() {
  const { jobs, loading, fetchJobs } = useJobs();

  useEffect(() => {
    fetchJobs().catch(() => {
      /* error surfaced via hook state */
    });
  }, [fetchJobs]);

  const getJobTypeLabel = (type: string) => {
    switch (type) {
      case "backfill":
        return "Mailbox Backfill";
      case "migration":
        return "Data Migration";
      case "oab-generation":
        return "OAB Generation";
      case "backup":
        return "Backup";
      case "restore":
        return "Restore";
      default:
        return type;
    }
  };

  const getJobTypeIcon = (type: string) => {
    switch (type) {
      case "backfill":
        return <Database className="h-4 w-4" />;
      case "migration":
        return <Upload className="h-4 w-4" />;
      case "oab-generation":
        return <Download className="h-4 w-4" />;
      case "backup":
        return <Download className="h-4 w-4" />;
      case "restore":
        return <Upload className="h-4 w-4" />;
      default:
        return <Clock className="h-4 w-4" />;
    }
  };

  const getStatusIcon = (status: string) => {
    switch (status) {
      case "pending":
        return <Clock className="h-4 w-4 text-muted-foreground" />;
      case "running":
        return <Loader2 className="h-4 w-4 text-blue-500 animate-spin" />;
      case "completed":
        return <CheckCircle className="h-4 w-4 text-emerald-500" />;
      case "failed":
        return <XCircle className="h-4 w-4 text-red-500" />;
      default:
        return <AlertCircle className="h-4 w-4 text-muted-foreground" />;
    }
  };

  const getStatusColor = (status: string) => {
    switch (status) {
      case "pending":
        return "bg-muted text-muted-foreground";
      case "running":
        return "bg-blue-500/10 text-blue-500";
      case "completed":
        return "bg-emerald-500/10 text-emerald-500";
      case "failed":
        return "bg-red-500/10 text-red-500";
      default:
        return "bg-muted text-muted-foreground";
    }
  };

  const activeJobs = jobs.filter((j) => j.status === "pending" || j.status === "running");
  const completedJobs = jobs.filter((j) => j.status === "completed" || j.status === "failed");

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Jobs</h1>
          <p className="text-muted-foreground mt-1">
            Monitor backfill, migration, OAB generation, and backup status
          </p>
        </div>
        <Button variant="outline" onClick={() => fetchJobs().catch(() => {})} disabled={loading}>
          <RefreshCw className={cn("mr-2 h-4 w-4", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* Active Jobs */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Briefcase className="h-5 w-5" />
            Active Jobs
          </CardTitle>
          <CardDescription>
            Currently running or pending jobs
          </CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="space-y-4">
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-24 w-full" />
            </div>
          ) : activeJobs.length === 0 ? (
            <div className="text-center py-8">
              <CheckCircle className="h-12 w-12 mx-auto text-emerald-500 mb-4" />
              <h3 className="text-lg font-medium">No active jobs</h3>
              <p className="text-muted-foreground mt-1">
                All jobs have completed or there are no pending jobs
              </p>
            </div>
          ) : (
            <div className="space-y-4">
              {activeJobs.map((job) => (
                <div
                  key={job.id}
                  className="p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                >
                  <div className="flex items-center justify-between mb-4">
                    <div className="flex items-center gap-3">
                      <div className={cn("p-2 rounded-lg", getStatusColor(job.status))}>
                        {getJobTypeIcon(job.type)}
                      </div>
                      <div>
                        <div className="font-medium">{getJobTypeLabel(job.type)}</div>
                        <div className="text-sm text-muted-foreground">
                          {job.mailbox ? `Mailbox: ${job.mailbox}` : "System job"}
                          {job.startedAt && (
                            <span> | Started: {new Date(job.startedAt).toLocaleString()}</span>
                          )}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {getStatusIcon(job.status)}
                      <Badge
                        variant="secondary"
                        className={cn(
                          job.status === "running" && "bg-blue-500/10 text-blue-500",
                          job.status === "pending" && "bg-muted text-muted-foreground"
                        )}
                      >
                        {job.status}
                      </Badge>
                    </div>
                  </div>
                  <div className="space-y-2">
                    <div className="flex items-center justify-between text-sm">
                      <span>Progress</span>
                      <span>{job.progress}%</span>
                    </div>
                    <Progress value={job.progress} className="h-2" />
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {/* Job History */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Clock className="h-5 w-5" />
            Job History
          </CardTitle>
          <CardDescription>
            Completed and failed jobs
          </CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="space-y-4">
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : completedJobs.length === 0 ? (
            <div className="text-center py-8">
              <Clock className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
              <h3 className="text-lg font-medium">No job history</h3>
              <p className="text-muted-foreground mt-1">
                Completed and failed jobs will appear here
              </p>
            </div>
          ) : (
            <div className="space-y-3">
              {completedJobs.map((job) => (
                <div
                  key={job.id}
                  className="flex items-center justify-between p-4 rounded-lg border hover:bg-muted/50 transition-colors"
                >
                  <div className="flex items-center gap-3">
                    <div className={cn("p-2 rounded-lg", getStatusColor(job.status))}>
                      {job.status === "completed" ? (
                        <CheckCircle className="h-4 w-4" />
                      ) : (
                        <XCircle className="h-4 w-4" />
                      )}
                    </div>
                    <div>
                      <div className="font-medium">{getJobTypeLabel(job.type)}</div>
                      <div className="text-sm text-muted-foreground">
                        {job.mailbox ? `Mailbox: ${job.mailbox}` : "System job"}
                        {job.completedAt && (
                          <span> | Completed: {new Date(job.completedAt).toLocaleString()}</span>
                        )}
                      </div>
                      {job.error && (
                        <div className="text-xs text-red-500 mt-1">Error: {job.error}</div>
                      )}
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Progress value={job.progress} className="w-20 h-2" />
                    <Badge
                      variant="secondary"
                      className={cn(
                        job.status === "completed" && "bg-emerald-500/10 text-emerald-500",
                        job.status === "failed" && "bg-red-500/10 text-red-500"
                      )}
                    >
                      {job.status}
                    </Badge>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

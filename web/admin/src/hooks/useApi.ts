import { useState, useCallback } from "react";
import type {
  Account,
  Domain,
  QueueEntry,
  DelegationEntry,
  DirectoryObject,
  BookingPolicy,
  RoomList,
  PolicyRule,
  RateLimitConfig,
  MailboxDiagnostics,
  SubscriptionInfo,
  ProtocolFailure,
  Job,
  ServerConfig,
} from "@/types";

interface ApiError {
  message: string;
  status?: number;
}

interface UseApiOptions {
  onError?: (error: ApiError) => void;
  onSuccess?: () => void;
}

const API_BASE = "/api/v1";

// Token is now stored in HttpOnly cookie by the server
// No need to read from localStorage (more secure against XSS)
async function apiRequest<T>(
  endpoint: string,
  options: RequestInit = {}
): Promise<T> {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...options.headers,
    },
    credentials: "include", // Send HttpOnly cookies with requests
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: "Unknown error" }));
    throw { message: errorData.error || "Request failed", status: response.status };
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json() as Promise<T>;
}

export function useApi<T>() {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const execute = useCallback(
    async (endpoint: string, options?: RequestInit, opts?: UseApiOptions) => {
      setLoading(true);
      setError(null);

      try {
        const result = await apiRequest<T>(endpoint, options);
        setData(result);
        opts?.onSuccess?.();
        return result;
      } catch (err) {
        const apiError = err as ApiError;
        setError(apiError);
        opts?.onError?.(apiError);
        throw err;
      } finally {
        setLoading(false);
      }
    },
    []
  );

  return { data, loading, error, execute, setData };
}

// Domain API hooks
export function useDomains() {
  const [data, setData] = useState<Domain[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchDomains = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<Domain[]>("/domains");
      setData(result);
      return result;
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const createDomain = useCallback(async (name: string, maxAccounts?: number) => {
    const result = await apiRequest<Domain>("/domains", {
      method: "POST",
      body: JSON.stringify({ name, max_accounts: maxAccounts }),
    });
    await fetchDomains();
    return result;
  }, [fetchDomains]);

  const updateDomain = useCallback(async (name: string, updates: Partial<Domain>) => {
    const result = await apiRequest<Domain>(`/domains/${name}`, {
      method: "PUT",
      body: JSON.stringify(updates),
    });
    await fetchDomains();
    return result;
  }, [fetchDomains]);

  const deleteDomain = useCallback(async (name: string) => {
    await apiRequest(`/domains/${name}`, { method: "DELETE" });
    await fetchDomains();
  }, [fetchDomains]);

  return {
    domains: data,
    loading,
    error,
    fetchDomains,
    createDomain,
    updateDomain,
    deleteDomain,
  };
}

// Account API hooks
export function useAccounts() {
  const [data, setData] = useState<Account[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchAccounts = useCallback(async (domain?: string) => {
    setLoading(true);
    try {
      const url = domain ? `/accounts?domain=${domain}` : "/accounts";
      const result = await apiRequest<Account[]>(url);
      setData(result);
      return result;
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const createAccount = useCallback(async (email: string, password: string, isAdmin = false) => {
    const result = await apiRequest<Account>("/accounts", {
      method: "POST",
      body: JSON.stringify({ email, password, is_admin: isAdmin }),
    });
    await fetchAccounts();
    return result;
  }, [fetchAccounts]);

  const updateAccount = useCallback(async (email: string, updates: Partial<Account>) => {
    const result = await apiRequest<Account>(`/accounts/${email}`, {
      method: "PUT",
      body: JSON.stringify(updates),
    });
    await fetchAccounts();
    return result;
  }, [fetchAccounts]);

  const deleteAccount = useCallback(async (email: string) => {
    await apiRequest(`/accounts/${email}`, { method: "DELETE" });
    await fetchAccounts();
  }, [fetchAccounts]);

  return {
    accounts: data,
    loading,
    error,
    fetchAccounts,
    createAccount,
    updateAccount,
    deleteAccount,
  };
}

// Stats API hook
export function useStats() {
  const [stats, setStats] = useState<{
    domains: number;
    accounts: number;
    messages: number;
    queue_size: number;
  } | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchStats = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<{
        domains: number;
        accounts: number;
        messages: number;
        queue_size: number;
      }>("/stats");
      setStats(result);
      return result;
    } finally {
      setLoading(false);
    }
  }, []);

  return { stats, loading, fetchStats, setStats };
}

// Queue API hooks
export function useQueue() {
  const [data, setData] = useState<QueueEntry[] | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchQueue = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<QueueEntry[]>("/queue");
      setData(result);
      return result;
    } finally {
      setLoading(false);
    }
  }, []);

  const retryEntry = useCallback(async (id: string) => {
    await apiRequest(`/queue/${id}`, { method: "POST" });
    await fetchQueue();
  }, [fetchQueue]);

  const dropEntry = useCallback(async (id: string) => {
    await apiRequest(`/queue/${id}`, { method: "DELETE" });
    await fetchQueue();
  }, [fetchQueue]);

  return {
    entries: data,
    loading,
    fetchQueue,
    retryEntry,
    dropEntry,
  };
}

// Delegation API hooks
export interface DelegationCreatePayload {
  owner: string;
  grantee: string;
  rights: string[];
  canSendAs: boolean;
  canSendOnBehalf: boolean;
}

export function useDelegations() {
  const [data, setData] = useState<DelegationEntry[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchDelegations = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<{ delegations: DelegationEntry[] }>("/admin/delegations");
      setData(result.delegations ?? []);
      return result.delegations ?? [];
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const createDelegation = useCallback(async (payload: DelegationCreatePayload) => {
    const result = await apiRequest<DelegationEntry>("/admin/delegations", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    await fetchDelegations();
    return result;
  }, [fetchDelegations]);

  const deleteDelegation = useCallback(async (id: string) => {
    await apiRequest(`/admin/delegations/${id}`, { method: "DELETE" });
    await fetchDelegations();
  }, [fetchDelegations]);

  return {
    delegations: data,
    loading,
    error,
    fetchDelegations,
    createDelegation,
    deleteDelegation,
  };
}

// Directory (resources + booking policies + room lists) API hooks
export interface DirectoryCreatePayload {
  name: string;
  email: string;
  type: "room" | "equipment";
  capacity: number;
}

export interface DirectoryUpdatePayload {
  isHidden?: boolean;
  isBookable?: boolean;
  capacity?: number;
  allowRecurring?: boolean;
  maxDuration?: number;
  requiresApproval?: boolean;
  approvalDelegate?: string;
}

interface DirectoryResponse {
  resources: DirectoryObject[];
  booking_policies: BookingPolicy[];
  room_lists: RoomList[];
}

export function useDirectory() {
  const [resources, setResources] = useState<DirectoryObject[]>([]);
  const [bookingPolicies, setBookingPolicies] = useState<BookingPolicy[]>([]);
  const [roomLists, setRoomLists] = useState<RoomList[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchDirectory = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<DirectoryResponse>("/admin/directory");
      setResources(result.resources ?? []);
      setBookingPolicies(result.booking_policies ?? []);
      setRoomLists(result.room_lists ?? []);
      return result;
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const createResource = useCallback(async (payload: DirectoryCreatePayload) => {
    const result = await apiRequest<DirectoryObject>("/admin/directory", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    await fetchDirectory();
    return result;
  }, [fetchDirectory]);

  const updateResource = useCallback(async (id: string, patch: DirectoryUpdatePayload) => {
    const result = await apiRequest<DirectoryObject>(`/admin/directory/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(patch),
    });
    await fetchDirectory();
    return result;
  }, [fetchDirectory]);

  const deleteResource = useCallback(async (id: string) => {
    await apiRequest(`/admin/directory/${encodeURIComponent(id)}`, { method: "DELETE" });
    await fetchDirectory();
  }, [fetchDirectory]);

  const createRoomList = useCallback(async (name: string, rooms: string[]) => {
    const result = await apiRequest<RoomList>("/admin/directory/roomlists", {
      method: "POST",
      body: JSON.stringify({ name, rooms }),
    });
    await fetchDirectory();
    return result;
  }, [fetchDirectory]);

  const updateRoomList = useCallback(async (id: string, name: string, rooms: string[]) => {
    const result = await apiRequest<RoomList>(`/admin/directory/roomlists/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify({ name, rooms }),
    });
    await fetchDirectory();
    return result;
  }, [fetchDirectory]);

  const deleteRoomList = useCallback(async (id: string) => {
    await apiRequest(`/admin/directory/roomlists/${encodeURIComponent(id)}`, { method: "DELETE" });
    await fetchDirectory();
  }, [fetchDirectory]);

  return {
    resources,
    bookingPolicies,
    roomLists,
    loading,
    error,
    fetchDirectory,
    createResource,
    updateResource,
    deleteResource,
    createRoomList,
    updateRoomList,
    deleteRoomList,
  };
}

// Admin inbox-rules API hooks
export function useAdminRules() {
  const [rules, setRules] = useState<PolicyRule[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchRules = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<{ rules: PolicyRule[] }>("/admin/rules");
      setRules(result.rules ?? []);
      return result.rules ?? [];
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const toggleRule = useCallback(async (id: string, enabled: boolean) => {
    await apiRequest(`/admin/rules/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    });
    await fetchRules();
  }, [fetchRules]);

  const deleteRule = useCallback(async (id: string) => {
    await apiRequest(`/admin/rules/${encodeURIComponent(id)}`, { method: "DELETE" });
    await fetchRules();
  }, [fetchRules]);

  return { rules, loading, error, fetchRules, toggleRule, deleteRule };
}

// Rate-limit config (flat, read-only display) API hook
export function useRateLimitConfig() {
  const [config, setConfig] = useState<RateLimitConfig | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchRateLimitConfig = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<RateLimitConfig>("/admin/ratelimits/config");
      setConfig(result);
      return result;
    } finally {
      setLoading(false);
    }
  }, []);

  return { config, loading, fetchRateLimitConfig };
}

// Admin diagnostics API hooks
interface DiagnosticsResponse {
  mailboxes: MailboxDiagnostics[];
  subscriptions: SubscriptionInfo[];
  failures: ProtocolFailure[];
}

export function useDiagnostics() {
  const [mailboxes, setMailboxes] = useState<MailboxDiagnostics[]>([]);
  const [subscriptions, setSubscriptions] = useState<SubscriptionInfo[]>([]);
  const [failures, setFailures] = useState<ProtocolFailure[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchDiagnostics = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<DiagnosticsResponse>("/admin/diagnostics");
      setMailboxes(result.mailboxes ?? []);
      setSubscriptions(result.subscriptions ?? []);
      setFailures(result.failures ?? []);
      return result;
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const fetchMailboxDetail = useCallback(async (email: string) => {
    return apiRequest<MailboxDiagnostics>(`/admin/diagnostics/${encodeURIComponent(email)}`);
  }, []);

  return { mailboxes, subscriptions, failures, loading, error, fetchDiagnostics, fetchMailboxDetail };
}

// Admin jobs API hook
export function useJobs() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchJobs = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<{ jobs: Job[] }>("/admin/jobs");
      setJobs(result.jobs ?? []);
      return result.jobs ?? [];
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  return { jobs, loading, error, fetchJobs };
}

// Server config (Settings) API hook
export interface ConfigUpdateResult {
  status: string;
  applied: string[];
  restart_required: string[];
  message: string;
}

export function useConfig() {
  const [config, setConfig] = useState<ServerConfig | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const fetchConfig = useCallback(async () => {
    setLoading(true);
    try {
      const result = await apiRequest<ServerConfig>("/admin/config");
      setConfig(result);
      return result;
    } catch (err) {
      setError(err as ApiError);
      throw err;
    } finally {
      setLoading(false);
    }
  }, []);

  const updateConfig = useCallback(async (cfg: ServerConfig) => {
    return apiRequest<ConfigUpdateResult>("/admin/config", {
      method: "PUT",
      body: JSON.stringify(cfg),
    });
  }, []);

  return { config, setConfig, loading, error, fetchConfig, updateConfig };
}

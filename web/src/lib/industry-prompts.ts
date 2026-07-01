import { httpRequest } from "@/lib/request";

// ---- Types shared by admin and user surfaces ----

export type IndustryPromptPreset = {
  id: string;
  industry_key: string;
  label: string;
  description?: string;
  prompt: string;
  sort_order: number;
  enabled: boolean;
  version: number;
  created_at: string;
  updated_at: string;
  created_by?: string;
  updated_by?: string;
};

export type IndustryPromptUserItem = {
  industry_key: string;
  label: string;
  description?: string;
  version: number;
  sort_order: number;
  has_override: boolean;
  resolved_prompt: string;
};

export type IndustryPromptUserDetail = {
  industry_key: string;
  label: string;
  description?: string;
  version: number;
  public_prompt: string;
  user_prompt: string;
  has_override: boolean;
  enabled: boolean;
  resolved_prompt: string;
};

// ---- Admin CRUD (menu 行业提示词) ----

export async function fetchAdminIndustryPrompts(params?: { search?: string; status?: string }) {
  const query: string[] = [];
  if (params?.search) query.push(`search=${encodeURIComponent(params.search)}`);
  if (params?.status) query.push(`status=${encodeURIComponent(params.status)}`);
  const suffix = query.length ? `?${query.join("&")}` : "";
  return httpRequest<{ items: IndustryPromptPreset[]; total: number; overrides_count_by_key: Record<string, number> }>(
    `/api/admin/industry-prompts${suffix}`,
  );
}

export async function createIndustryPromptPreset(body: {
  industry_key: string;
  label: string;
  description?: string;
  prompt: string;
  sort_order?: number;
  enabled?: boolean;
}) {
  return httpRequest<{ item: IndustryPromptPreset; items: IndustryPromptPreset[] }>("/api/admin/industry-prompts", {
    method: "POST",
    body,
  });
}

export async function updateIndustryPromptPreset(
  id: string,
  body: Partial<Pick<IndustryPromptPreset, "label" | "description" | "prompt" | "sort_order" | "enabled">>,
) {
  return httpRequest<{ item: IndustryPromptPreset; items: IndustryPromptPreset[] }>(
    `/api/admin/industry-prompts/${id}`,
    { method: "POST", body },
  );
}

export async function deleteIndustryPromptPreset(id: string) {
  return httpRequest<{ items: IndustryPromptPreset[] }>(`/api/admin/industry-prompts/${id}`, { method: "DELETE" });
}

export async function exportIndustryPromptPresets() {
  return httpRequest<{ items: IndustryPromptPreset[] }>("/api/admin/industry-prompts/export");
}

export async function importIndustryPromptPresets(items: Partial<IndustryPromptPreset>[]) {
  return httpRequest<{ created: number; updated: number; items: IndustryPromptPreset[] }>(
    "/api/admin/industry-prompts/import",
    { method: "POST", body: { items } },
  );
}

// ---- Profile (user) endpoints ----

export async function fetchProfileIndustryPrompts() {
  return httpRequest<{ items: IndustryPromptUserItem[] }>("/api/profile/industry-prompts");
}

export async function fetchProfileIndustryPrompt(industryKey: string) {
  return httpRequest<{ item: IndustryPromptUserDetail }>(
    `/api/profile/industry-prompts/${encodeURIComponent(industryKey)}`,
  );
}

export async function saveProfileIndustryPromptOverride(industryKey: string, prompt: string) {
  return httpRequest<{ override: { prompt: string; based_on_version: number; updated_at: string } }>(
    `/api/profile/industry-prompts/${encodeURIComponent(industryKey)}`,
    { method: "PUT", body: { prompt } },
  );
}

export async function deleteProfileIndustryPromptOverride(industryKey: string) {
  return httpRequest<{ ok: boolean }>(`/api/profile/industry-prompts/${encodeURIComponent(industryKey)}`, {
    method: "DELETE",
  });
}

export async function fetchCurrentIndustry() {
  return httpRequest<{ industry_key: string; effective: boolean }>("/api/profile/current-industry");
}

export async function setCurrentIndustry(industryKey: string) {
  return httpRequest<{ industry_key: string; effective: boolean }>("/api/profile/current-industry", {
    method: "PUT",
    body: { industry_key: industryKey },
  });
}

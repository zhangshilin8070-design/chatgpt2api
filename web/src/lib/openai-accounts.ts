import { httpRequest } from "@/lib/request";
import type { AccountStatus } from "@/lib/api";

/**
 * Upstream_Image_Model：OpenAI 协议账号 `allowed_models` 的合法取值集合，
 * 与后端 `internal/util/json.go` 中的 `ImageModelGPTImage2` /
 * `ImageModelGeminiFlashImage` 常量一一对应。
 *
 * 注意：对外模型 `codex-gpt-image-2` 仅出现在路由层，不进入此集合。
 */
export type OpenAIAccountUpstreamModel = "gpt-image-2" | "gemini-3.1-flash-image";

export const OPENAI_ACCOUNT_UPSTREAM_MODELS: readonly OpenAIAccountUpstreamModel[] = [
  "gpt-image-2",
  "gemini-3.1-flash-image",
] as const;

/**
 * 单个 Upstream_Image_Model 在某条账号下的运行时状态视图。
 *
 * 字段语义见 `internal/service/openai_account.go::defaultOpenAIAccountModelState`：
 *  - `status`         上游可调度状态（`正常` / `限流` / `异常` / `禁用`）
 *  - `last_used_at`   ISO-8601 字符串；从未使用过时为空串
 *  - `success` / `fail` 累计成功 / 失败计数
 *  - `error_message`  最近一次失败的归因；成功后会被清空
 */
export type OpenAIAccountModelState = {
  status: AccountStatus;
  last_used_at: string;
  success: number;
  fail: number;
  error_message: string;
};

/**
 * OpenAI 协议账号脱敏视图。`api_key` 始终以 `sk-***{last4}` 形式返回，
 * 由后端 `redactAPIKey` 处理；前端不应假设它可以反向还原。
 */
export type OpenAIAccount = {
  id: string;
  name: string;
  api_key: string;
  base_url: string;
  allowed_models: OpenAIAccountUpstreamModel[];
  priority: number;
  concurrency: number;
  model_states: Partial<Record<OpenAIAccountUpstreamModel, OpenAIAccountModelState>>;
  created_at: string;
  updated_at: string;
};

/**
 * 新增 / 编辑 OpenAI 协议账号的入参。所有字段在前端层面都设为可选：
 *  - 新增场景：`name` / `api_key` / `base_url` / `allowed_models` 必须由调用方填齐，
 *    否则后端返回 400（`api_key is required` 等），由调用方在表单层面提前校验。
 *  - 编辑场景：`api_key` 留空表示保留旧值；其它字段一旦提供就会被服务端校验并写盘。
 */
export type OpenAIAccountInput = {
  name?: string;
  api_key?: string;
  base_url?: string;
  allowed_models?: OpenAIAccountUpstreamModel[];
  priority?: number;
  concurrency?: number;
};

/**
 * `PATCH /api/openai-accounts/{id}/model-states/{model}` 的入参。后端只接受
 * `status` 与 `error_message` 两个键，其它键被服务端忽略（参见
 * `OpenAIAccountService.UpdateModelState`）。
 */
export type OpenAIAccountModelStatePatch = {
  status?: AccountStatus;
  error_message?: string;
};

type OpenAIAccountListResponse = {
  items: OpenAIAccount[];
};

type OpenAIAccountMutationResponse = {
  item: OpenAIAccount;
  items: OpenAIAccount[];
};

type OpenAIAccountDeleteResponse = {
  items: OpenAIAccount[];
};

const OPENAI_ACCOUNTS_PATH = "/api/openai-accounts";

/**
 * 拉取 OpenAI 协议账号池脱敏列表。
 *
 * 对应后端 `GET /api/openai-accounts`。
 */
export async function listOpenAIAccounts() {
  return httpRequest<OpenAIAccountListResponse>(OPENAI_ACCOUNTS_PATH);
}

/**
 * 创建一条 OpenAI 协议账号。
 *
 * 对应后端 `POST /api/openai-accounts`。`allowed_models` 必须是
 * `{gpt-image-2, gemini-3.1-flash-image}` 的非空子集。
 */
export async function createOpenAIAccount(input: OpenAIAccountInput) {
  return httpRequest<OpenAIAccountMutationResponse>(OPENAI_ACCOUNTS_PATH, {
    method: "POST",
    body: input,
  });
}

/**
 * 更新一条 OpenAI 协议账号的部分字段。
 *
 * 对应后端 `PATCH /api/openai-accounts/{id}`。当 `api_key` 为空字符串或缺省时
 * 后端保留旧值，便于编辑表单不强制重新输入密钥。
 */
export async function updateOpenAIAccount(id: string, patch: OpenAIAccountInput) {
  const path = `${OPENAI_ACCOUNTS_PATH}/${encodeURIComponent(id)}`;
  return httpRequest<OpenAIAccountMutationResponse>(path, {
    method: "PATCH",
    body: patch,
  });
}

/**
 * 删除一条 OpenAI 协议账号。
 *
 * 对应后端 `DELETE /api/openai-accounts/{id}`。后端会同步释放该账号上未结束的
 * 并发槽位预留。
 */
export async function deleteOpenAIAccount(id: string) {
  const path = `${OPENAI_ACCOUNTS_PATH}/${encodeURIComponent(id)}`;
  return httpRequest<OpenAIAccountDeleteResponse>(path, {
    method: "DELETE",
  });
}

/**
 * 更新某条账号上指定 Upstream_Image_Model 的运行时状态（启用 / 禁用 / 限流等）。
 *
 * 对应后端 `PATCH /api/openai-accounts/{id}/model-states/{model}`。
 * 该操作仅作用于该账号-模型对，不影响该账号承载的其它模型。
 */
export async function updateOpenAIAccountModelState(
  id: string,
  model: OpenAIAccountUpstreamModel,
  patch: OpenAIAccountModelStatePatch,
) {
  const path = `${OPENAI_ACCOUNTS_PATH}/${encodeURIComponent(id)}/model-states/${encodeURIComponent(model)}`;
  return httpRequest<OpenAIAccountMutationResponse>(path, {
    method: "PATCH",
    body: patch,
  });
}

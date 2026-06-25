export const CUSTOM_IMAGE_ASPECT_RATIO = "custom";
export const DEFAULT_IMAGE_CUSTOM_RATIO = "16:9";

export const IMAGE_ASPECT_RATIO_OPTIONS = [
  { value: "", label: "Auto" },
  { value: "1:1", label: "1:1 (正方形)" },
  { value: "3:2", label: "3:2 (横版)" },
  { value: "2:3", label: "2:3 (竖版)" },
  { value: "16:9", label: "16:9 (横版)" },
  { value: "21:9", label: "21:9 (超宽横版)" },
  { value: "4:3", label: "4:3 (横版)" },
  { value: "3:4", label: "3:4 (竖版)" },
  { value: "9:16", label: "9:16 (竖版)" },
  { value: CUSTOM_IMAGE_ASPECT_RATIO, label: "自定义比例" },
] as const;

export type ImageAspectRatio = (typeof IMAGE_ASPECT_RATIO_OPTIONS)[number]["value"];

export const IMAGE_SIZE_MODE_OPTIONS = [
  { value: "auto", label: "Auto" },
  { value: "ratio", label: "按比例" },
  { value: "custom", label: "手动宽高" },
] as const;

export type ImageSizeMode = (typeof IMAGE_SIZE_MODE_OPTIONS)[number]["value"];

export const IMAGE_RESOLUTION_OPTIONS = [
  { value: "auto", label: "Auto", description: "不指定固定像素，交给图片工具决定" },
  { value: "1080p", label: "1080P", description: "1080P 画质档（提示上游优先 1080P 级别构图，实际像素以上游返回为准）" },
  { value: "2k", label: "2K", description: "2K 画质档（提示上游优先 2K 级别构图，实际像素以上游返回为准）" },
  { value: "4k", label: "4K", description: "4K 画质档（提示上游优先 4K 级别构图，实际像素以上游返回为准）" },
] as const;

export type ImageResolution = (typeof IMAGE_RESOLUTION_OPTIONS)[number]["value"];

export type ImageSizeSelection = {
  mode: ImageSizeMode;
  aspectRatio: ImageAspectRatio;
  resolution: ImageResolution;
  customRatio: string;
  customWidth: string;
  customHeight: string;
};

const IMAGE_ASPECT_RATIO_VALUES = new Set<string>(IMAGE_ASPECT_RATIO_OPTIONS.map((option) => option.value));
const IMAGE_SIZE_MODE_VALUES = new Set<string>(IMAGE_SIZE_MODE_OPTIONS.map((option) => option.value));
const IMAGE_RESOLUTION_VALUES = new Set<string>(IMAGE_RESOLUTION_OPTIONS.map((option) => option.value));
const SIZE_PATTERN = /^\s*(\d+)\s*[xX×]\s*(\d+)\s*$/;
const RATIO_PATTERN = /^\s*(\d+(?:\.\d+)?)\s*[:xX×]\s*(\d+(?:\.\d+)?)\s*$/;
const SIZE_MULTIPLE = 16;
const MAX_EDGE = 3840;
const MAX_ASPECT_RATIO = 3;
const MIN_PIXELS = 655_360;
const MAX_PIXELS = 8_294_400;
const HIGH_RESOLUTION_PIXEL_THRESHOLD = 1_577_536;
export const DEFAULT_IMAGE_CUSTOM_WIDTH = "1024";
export const DEFAULT_IMAGE_CUSTOM_HEIGHT = "1024";

export const IMAGE_SIZE_PRESET_DETAILS = [
  { label: "1:1", requestValue: "1:1", normalizedSize: "1024x1024", highResolution: false },
  { label: "3:2", requestValue: "3:2", normalizedSize: "1536x1024", highResolution: false },
  { label: "2:3", requestValue: "2:3", normalizedSize: "1024x1536", highResolution: false },
  { label: "16:9", requestValue: "16:9", normalizedSize: "1536x1024", highResolution: false },
  { label: "9:16", requestValue: "9:16", normalizedSize: "1024x1536", highResolution: false },
] as const;

export const IMAGE_QUALITY_OPTIONS = [
  { value: "low", label: "Low", description: "低质量，速度更快，适合草稿测试" },
  { value: "medium", label: "Medium", description: "均衡质量与速度，适合日常生成" },
  { value: "high", label: "High", description: "高质量，耗时更长，适合最终出图" },
] as const;

function roundToMultiple(value: number, multiple: number) {
  return Math.max(multiple, Math.round(value / multiple) * multiple);
}

function floorToMultiple(value: number, multiple: number) {
  return Math.max(multiple, Math.floor(value / multiple) * multiple);
}

function ceilToMultiple(value: number, multiple: number) {
  return Math.max(multiple, Math.ceil(value / multiple) * multiple);
}

function normalizeDimensions(width: number, height: number) {
  let normalizedWidth = roundToMultiple(width, SIZE_MULTIPLE);
  let normalizedHeight = roundToMultiple(height, SIZE_MULTIPLE);

  const scaleToFit = (scale: number) => {
    normalizedWidth = floorToMultiple(normalizedWidth * scale, SIZE_MULTIPLE);
    normalizedHeight = floorToMultiple(normalizedHeight * scale, SIZE_MULTIPLE);
  };
  const scaleToFill = (scale: number) => {
    normalizedWidth = ceilToMultiple(normalizedWidth * scale, SIZE_MULTIPLE);
    normalizedHeight = ceilToMultiple(normalizedHeight * scale, SIZE_MULTIPLE);
  };

  for (let index = 0; index < 4; index += 1) {
    const maxEdge = Math.max(normalizedWidth, normalizedHeight);
    if (maxEdge > MAX_EDGE) {
      scaleToFit(MAX_EDGE / maxEdge);
    }

    if (normalizedWidth / normalizedHeight > MAX_ASPECT_RATIO) {
      normalizedWidth = floorToMultiple(normalizedHeight * MAX_ASPECT_RATIO, SIZE_MULTIPLE);
    } else if (normalizedHeight / normalizedWidth > MAX_ASPECT_RATIO) {
      normalizedHeight = floorToMultiple(normalizedWidth * MAX_ASPECT_RATIO, SIZE_MULTIPLE);
    }

    const pixels = normalizedWidth * normalizedHeight;
    if (pixels > MAX_PIXELS) {
      scaleToFit(Math.sqrt(MAX_PIXELS / pixels));
    } else if (pixels < MIN_PIXELS) {
      scaleToFill(Math.sqrt(MIN_PIXELS / pixels));
    }
  }

  return { width: normalizedWidth, height: normalizedHeight };
}

export function normalizeImageSize(size: string) {
  const trimmed = size.trim();
  const match = trimmed.match(SIZE_PATTERN);
  if (!match) {
    return trimmed;
  }

  const width = Number(match[1]);
  const height = Number(match[2]);
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return "";
  }

  const normalized = normalizeDimensions(width, height);
  return `${normalized.width}x${normalized.height}`;
}

export function parseImageSizeDimensions(size: string) {
  const match = normalizeImageSize(size).match(SIZE_PATTERN);
  if (!match) {
    return null;
  }
  return { width: match[1], height: match[2] };
}

export function imageSizePixels(size: string) {
  const dimensions = parseImageSizeDimensions(size);
  if (!dimensions) {
    return 0;
  }
  return Number(dimensions.width) * Number(dimensions.height);
}

export function isHighResolutionImageSize(size: string) {
  return imageSizePixels(size) > HIGH_RESOLUTION_PIXEL_THRESHOLD;
}

export function parseImageRatio(ratio: string) {
  const match = ratio.match(RATIO_PATTERN);
  if (!match) {
    return null;
  }
  const width = Number(match[1]);
  const height = Number(match[2]);
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    return null;
  }
  return { width, height };
}

export function getActiveImageAspectRatio({
  aspectRatio,
  customRatio,
}: Pick<ImageSizeSelection, "aspectRatio" | "customRatio">) {
  if (aspectRatio === CUSTOM_IMAGE_ASPECT_RATIO) {
    return parseImageRatio(customRatio) ? customRatio.trim() : "";
  }
  return aspectRatio;
}

export function buildCustomImageSize(width: string, height: string) {
  const parsedWidth = Number.parseInt(width, 10);
  const parsedHeight = Number.parseInt(height, 10);
  if (!Number.isFinite(parsedWidth) || !Number.isFinite(parsedHeight) || parsedWidth <= 0 || parsedHeight <= 0) {
    return "";
  }
  return normalizeImageSize(`${parsedWidth}x${parsedHeight}`);
}

export function formatImageSizeDisplay(size: string) {
  return size.replace(/x/g, "×");
}

export function getImageSizeRequirementLabel(size: string) {
  if (!size || size === "auto") {
    return "Auto";
  }
  return isHighResolutionImageSize(size) ? "高分辨率" : "常规分辨率";
}

export function isImageAspectRatio(value: unknown): value is ImageAspectRatio {
  return typeof value === "string" && IMAGE_ASPECT_RATIO_VALUES.has(value);
}

export function isImageSizeMode(value: unknown): value is ImageSizeMode {
  return typeof value === "string" && IMAGE_SIZE_MODE_VALUES.has(value);
}

export function isImageResolution(value: unknown): value is ImageResolution {
  return typeof value === "string" && IMAGE_RESOLUTION_VALUES.has(value);
}

// 当 resolution !== "auto" 时，size 字段只承载比例语义（"1:1" / "16:9" …）
// 或显式像素；分档 1080p / 2k / 4k 单独通过 `image_resolution` 字段透传，
// 不再合成具体像素。原因详见 internal/backend/responses_image_size.go：
// codex/responses 与 OpenAI Images 上游只接受 1024x1024 / 1536x1024 /
// 1024x1536 / auto，超出值会被打回 5xx；分档信息靠 prompt 提示传递。
export function buildImageSize({
  mode,
  aspectRatio,
  customRatio,
  customWidth,
  customHeight,
}: ImageSizeSelection) {
  if (mode === "auto") {
    return "";
  }
  if (mode === "custom") {
    return buildCustomImageSize(customWidth, customHeight);
  }
  const activeAspectRatio = getActiveImageAspectRatio({ aspectRatio, customRatio });
  if (aspectRatio === CUSTOM_IMAGE_ASPECT_RATIO && !activeAspectRatio) {
    return "";
  }
  return activeAspectRatio;
}

export function getImageAspectRatioFromSize(size: string): ImageAspectRatio {
  const normalized = normalizeImageSize(size);
  if (isImageAspectRatio(normalized) && normalized !== CUSTOM_IMAGE_ASPECT_RATIO) {
    return normalized;
  }
  const isDimensionSize = SIZE_PATTERN.test(normalized);
  if (!isDimensionSize && parseImageRatio(normalized)) {
    return CUSTOM_IMAGE_ASPECT_RATIO;
  }
  return "";
}

export function getImageResolutionFromSize(size: string): ImageResolution {
  const normalized = normalizeImageSize(size);
  if (isImageResolution(normalized)) {
    return normalized;
  }
  return "auto";
}

export function getImageSizeSelectionFromSize(size: string): ImageSizeSelection {
  const normalized = normalizeImageSize(size);
  const customSize = parseImageSizeDimensions(normalized);
  const aspectRatio = getImageAspectRatioFromSize(normalized);
  const resolution = getImageResolutionFromSize(normalized);
  const customRatio = aspectRatio === CUSTOM_IMAGE_ASPECT_RATIO ? normalized : DEFAULT_IMAGE_CUSTOM_RATIO;
  const baseSelection = {
    aspectRatio,
    resolution,
    customRatio,
    customWidth: customSize?.width ?? DEFAULT_IMAGE_CUSTOM_WIDTH,
    customHeight: customSize?.height ?? DEFAULT_IMAGE_CUSTOM_HEIGHT,
  };

  if (!normalized || normalized === "auto") {
    return {
      mode: "auto",
      aspectRatio: "",
      resolution: "auto",
      customRatio: baseSelection.customRatio,
      customWidth: baseSelection.customWidth,
      customHeight: baseSelection.customHeight,
    };
  }
  if (customSize && !aspectRatio && resolution === "auto") {
    return {
      ...baseSelection,
      mode: "custom",
    };
  }
  return {
    ...baseSelection,
    mode: "ratio",
  };
}

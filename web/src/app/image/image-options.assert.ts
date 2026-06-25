import assert from "node:assert/strict";

import {
  CUSTOM_IMAGE_ASPECT_RATIO,
  buildCustomImageSize,
  buildImageSize,
  getImageSizeSelectionFromSize,
  isHighResolutionImageSize,
  parseImageRatio,
  type ImageSizeSelection,
} from "./image-options";

function ratioSelection(overrides: Partial<ImageSizeSelection> = {}): ImageSizeSelection {
  return {
    mode: "ratio",
    aspectRatio: "16:9",
    resolution: "2k",
    customRatio: "2.39:1",
    customWidth: "1024",
    customHeight: "1024",
    ...overrides,
  };
}

assert.deepEqual(parseImageRatio("2.39:1"), { width: 2.39, height: 1 });
assert.equal(buildCustomImageSize("999", "777"), "992x784");

// buildImageSize 只发比例 / 显式像素，分档 1080p/2k/4k 通过单独的
// image_resolution 字段透传，不再合成像素。详见 image-options.ts 注释。
assert.equal(buildImageSize(ratioSelection()), "16:9");
assert.equal(buildImageSize(ratioSelection({ resolution: "auto" })), "16:9");
assert.equal(buildImageSize(ratioSelection({ resolution: "4k" })), "16:9");
assert.equal(
  buildImageSize(ratioSelection({ aspectRatio: CUSTOM_IMAGE_ASPECT_RATIO })),
  "2.39:1",
);
assert.equal(
  buildImageSize(ratioSelection({ aspectRatio: CUSTOM_IMAGE_ASPECT_RATIO, customRatio: "invalid" })),
  "",
);
assert.equal(
  buildImageSize(ratioSelection({ aspectRatio: "", resolution: "4k" })),
  "",
);
assert.equal(buildImageSize({ ...ratioSelection(), mode: "auto" }), "");
assert.equal(
  buildImageSize({ ...ratioSelection(), mode: "custom", customWidth: "1280", customHeight: "720" }),
  "1280x720",
);

assert.equal(isHighResolutionImageSize("1088x1088"), false);
assert.equal(isHighResolutionImageSize("2048x2048"), true);

assert.deepEqual(getImageSizeSelectionFromSize("2.39:1"), {
  mode: "ratio",
  aspectRatio: CUSTOM_IMAGE_ASPECT_RATIO,
  resolution: "auto",
  customRatio: "2.39:1",
  customWidth: "1024",
  customHeight: "1024",
});

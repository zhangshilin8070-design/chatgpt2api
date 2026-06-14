package backend

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"chatgpt2api/internal/util"
)

func supportsResponsesImageOutputCompression(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return true
	default:
		return false
	}
}

func normalizeResponsesImageToolModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", util.ImageModelAuto, "gpt-image-1", util.ImageModelGPTImage2:
		return ""
	case util.ImageModelCodexGPTImage2:
		return ResponsesImageCodexToolModel
	case ResponsesImageCodexToolModel:
		return ResponsesImageCodexToolModel
	case util.ImageModelGPT54:
		return util.ImageModelGPT54
	case util.ImageModelGPT55:
		return util.ImageModelGPT55
	case "gpt-5-5-thinking":
		return "gpt-5-5-thinking"
	default:
		return ""
	}
}

func normalizeResponsesImageToolSize(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "×", "x")
	if normalized == "" || normalized == "auto" {
		return ""
	}
	switch normalized {
	case "1080p":
		return normalizeResponsesImageDimensions(1080, 1080)
	case "2k":
		return normalizeResponsesImageDimensions(2048, 2048)
	case "4k":
		return normalizeResponsesImageDimensions(3840, 3840)
	}
	if width, height, ok := parseResponsesImageDimensions(normalized); ok {
		if width < 128 && height < 128 {
			return responsesImageSizeFromRatio(float64(width), float64(height))
		}
		return normalizeResponsesImageDimensions(width, height)
	}
	if ratioWidth, ratioHeight, ok := parseResponsesImageRatio(normalized); ok {
		return responsesImageSizeFromRatio(ratioWidth, ratioHeight)
	}
	return ""
}

func parseResponsesImageDimensions(value string) (int, int, bool) {
	match := regexp.MustCompile(`^(\d+)x(\d+)$`).FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, 0, false
	}
	width, err := strconv.Atoi(match[1])
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.Atoi(match[2])
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func parseResponsesImageRatio(value string) (float64, float64, bool) {
	match := regexp.MustCompile(`^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$`).FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, 0, false
	}
	width, err := strconv.ParseFloat(match[1], 64)
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.ParseFloat(match[2], 64)
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func responsesImageSizeFromRatio(ratioWidth, ratioHeight float64) string {
	if ratioWidth <= 0 || ratioHeight <= 0 {
		return ""
	}
	if ratioWidth == ratioHeight {
		return responsesImageDefaultSize
	}
	if ratioWidth > ratioHeight {
		return normalizeResponsesImageDimensions(1536, int(float64(1536)*ratioHeight/ratioWidth+0.5))
	}
	return normalizeResponsesImageDimensions(int(float64(1536)*ratioWidth/ratioHeight+0.5), 1536)
}

func normalizeResponsesImageDimensions(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	normalizedWidth := roundToResponsesImageMultiple(width)
	normalizedHeight := roundToResponsesImageMultiple(height)

	scaleToFit := func(scale float64) {
		normalizedWidth = floorToResponsesImageMultiple(float64(normalizedWidth) * scale)
		normalizedHeight = floorToResponsesImageMultiple(float64(normalizedHeight) * scale)
	}
	scaleToFill := func(scale float64) {
		normalizedWidth = ceilToResponsesImageMultiple(float64(normalizedWidth) * scale)
		normalizedHeight = ceilToResponsesImageMultiple(float64(normalizedHeight) * scale)
	}

	for range 4 {
		maxEdge := max(normalizedWidth, normalizedHeight)
		if maxEdge > responsesImageMaxEdge {
			scaleToFit(float64(responsesImageMaxEdge) / float64(maxEdge))
		}
		if normalizedWidth > normalizedHeight*responsesImageMaxRatio {
			normalizedWidth = floorToResponsesImageMultiple(float64(normalizedHeight * responsesImageMaxRatio))
		} else if normalizedHeight > normalizedWidth*responsesImageMaxRatio {
			normalizedHeight = floorToResponsesImageMultiple(float64(normalizedWidth * responsesImageMaxRatio))
		}
		pixels := normalizedWidth * normalizedHeight
		if pixels > responsesImageMaxPixels {
			scaleToFit(math.Sqrt(float64(responsesImageMaxPixels) / float64(pixels)))
		} else if pixels < responsesImageMinPixels {
			scaleToFill(math.Sqrt(float64(responsesImageMinPixels) / float64(pixels)))
		}
	}

	return fmt.Sprintf("%dx%d", normalizedWidth, normalizedHeight)
}

func roundToResponsesImageMultiple(value int) int {
	return max(responsesImageSizeMultiple, ((value+responsesImageSizeMultiple/2)/responsesImageSizeMultiple)*responsesImageSizeMultiple)
}

func floorToResponsesImageMultiple(value float64) int {
	return max(responsesImageSizeMultiple, int(value/float64(responsesImageSizeMultiple))*responsesImageSizeMultiple)
}

func ceilToResponsesImageMultiple(value float64) int {
	base := int(value / float64(responsesImageSizeMultiple))
	if float64(base*responsesImageSizeMultiple) < value {
		base++
	}
	return max(responsesImageSizeMultiple, base*responsesImageSizeMultiple)
}

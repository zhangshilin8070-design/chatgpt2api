package service

import (
	"image"
	"os"
	"strconv"
)

func setImageItemDimensions(item map[string]any, widthValue, heightValue any) bool {
	width, height, ok := imageDimensionsFromValues(widthValue, heightValue)
	if !ok {
		return false
	}
	item["width"] = width
	item["height"] = height
	item["resolution"] = strconv.Itoa(width) + "x" + strconv.Itoa(height)
	item["aspect_ratio"] = simplifiedAspectRatio(width, height)
	item["orientation"] = imageOrientation(width, height)
	item["megapixels"] = float64(width) * float64(height) / 1_000_000
	return true
}

func imageDimensionsFromValues(widthValue, heightValue any) (int, int, bool) {
	width := numericMetaValue(widthValue)
	height := numericMetaValue(heightValue)
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func imageFileDimensions(path string) (int, int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func simplifiedAspectRatio(width, height int) string {
	divisor := greatestCommonDivisor(width, height)
	if divisor <= 0 {
		return ""
	}
	return strconv.Itoa(width/divisor) + ":" + strconv.Itoa(height/divisor)
}

func imageOrientation(width, height int) string {
	if width == height {
		return "square"
	}
	if width > height {
		return "landscape"
	}
	return "portrait"
}

func greatestCommonDivisor(a, b int) int {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

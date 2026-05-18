package recall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MediaMeta holds extracted metadata from images and videos.
type MediaMeta struct {
	Path     string    `json:"path"`
	Type     string    `json:"type"` // "image" or "video"
	Width    string    `json:"width"`
	Height   string    `json:"height"`
	Format   string    `json:"format"`   // "jpeg", "png", "mp4", etc.
	Duration string    `json:"duration"` // video only
	DateTime time.Time `json:"date_time,omitempty"`
	GPS      string    `json:"gps,omitempty"`
	Camera   string    `json:"camera,omitempty"`
	Size     int64     `json:"size"`
}

// isMediaFile checks if a file is an image or video.
func isMediaFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	media := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
		".heic": true, ".heif": true, ".bmp": true, ".tiff": true, ".svg": true,
		".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true,
		".m4v": true, ".3gp": true,
	}
	return media[ext]
}

// ExtractMediaMeta uses exiftool (if available) to get metadata,
// falls back to basic file info.
func ExtractMediaMeta(path string) *MediaMeta {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	meta := &MediaMeta{
		Path:   path,
		Format: strings.TrimPrefix(ext, "."),
		Size:   info.Size(),
	}

	// Classify
	switch ext {
	case ".mp4", ".mov", ".avi", ".mkv", ".webm", ".m4v", ".3gp":
		meta.Type = "video"
	default:
		meta.Type = "image"
	}

	// Try exiftool for rich metadata
	if exif, err := exec.LookPath("exiftool"); err == nil {
		out, err := exec.Command(exif, "-s", "-s", "-s",
			"-ImageWidth", "-ImageHeight", "-DateTimeOriginal",
			"-GPSPosition", "-Model", "-Duration",
			path).Output()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			fields := []string{"width", "height", "datetime", "gps", "camera", "duration"}
			for i, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || i >= len(fields) {
					continue
				}
				switch fields[i] {
				case "width":
					meta.Width = line
				case "height":
					meta.Height = line
				case "datetime":
					if t, err := time.Parse("2006:01:02 15:04:05", line); err == nil {
						meta.DateTime = t
					}
				case "gps":
					meta.GPS = line
				case "camera":
					meta.Camera = line
				case "duration":
					meta.Duration = line
				}
			}
		}
	}

	// Fallback: use identify (ImageMagick) for dimensions
	if meta.Width == "" && meta.Type == "image" {
		if identify, err := exec.LookPath("identify"); err == nil {
			out, err := exec.Command(identify, "-format", "%wx%h", path).Output()
			if err == nil {
				parts := strings.Split(strings.TrimSpace(string(out)), "x")
				if len(parts) == 2 {
					meta.Width = parts[0]
					meta.Height = parts[1]
				}
			}
		}
	}

	return meta
}

// MediaMetaToText converts metadata to a searchable text representation
// that can be embedded by the vector DB.
func MediaMetaToText(meta *MediaMeta) string {
	parts := []string{
		fmt.Sprintf("%s file: %s", meta.Type, filepath.Base(meta.Path)),
		fmt.Sprintf("format: %s, size: %d bytes", meta.Format, meta.Size),
	}
	if meta.Width != "" && meta.Height != "" {
		parts = append(parts, fmt.Sprintf("dimensions: %sx%s", meta.Width, meta.Height))
	}
	if !meta.DateTime.IsZero() {
		parts = append(parts, fmt.Sprintf("taken: %s", meta.DateTime.Format("Monday, January 2, 2006 at 3:04 PM")))
	}
	if meta.GPS != "" {
		parts = append(parts, fmt.Sprintf("location: %s", meta.GPS))
	}
	if meta.Camera != "" {
		parts = append(parts, fmt.Sprintf("camera: %s", meta.Camera))
	}
	if meta.Duration != "" {
		parts = append(parts, fmt.Sprintf("duration: %s", meta.Duration))
	}
	return strings.Join(parts, ". ")
}

package render

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// family is the face drawn by default. The Debian package fonts-noto-cjk ships
// it in two weights, which is why the layouts use two.
const family = "Noto Sans CJK SC"

var fontDirs = []string{
	"/usr/share/fonts/opentype/noto",
	"/usr/share/fonts/truetype/noto",
	"/usr/share/fonts/noto-cjk",
}

type face struct {
	font *sfnt.Font
	upem float64
	// ascent and descent are in font units; ascent is negative.
	ascent, descent float64
}

// faceSet holds one face per Weight.
type faceSet [2]face

var (
	facesMu    sync.Mutex
	facesCache = map[string]*faceSet{}
)

// loadFaces returns the faces for an administrator-chosen font file, which
// serves both weights, or Noto Sans CJK SC when fontPath is empty. Loaded
// fonts stay cached: a CJK collection takes tens of megabytes to parse.
func loadFaces(fontPath string) (*faceSet, error) {
	fontPath = strings.TrimSpace(fontPath)
	facesMu.Lock()
	defer facesMu.Unlock()
	if faces, ok := facesCache[fontPath]; ok {
		return faces, nil
	}
	faces := &faceSet{}
	if fontPath != "" {
		custom, err := loadFace(fontPath, "")
		if err != nil {
			return nil, err
		}
		faces[Regular], faces[Bold] = custom, custom
	} else {
		for _, weight := range []Weight{Regular, Bold} {
			file, err := notoFile(weight)
			if err != nil {
				return nil, err
			}
			if faces[weight], err = loadFace(file, family); err != nil {
				return nil, err
			}
		}
	}
	facesCache[fontPath] = faces
	return faces, nil
}

func notoFile(w Weight) (string, error) {
	name := "NotoSansCJK-Regular.ttc"
	if w == Bold {
		name = "NotoSansCJK-Bold.ttc"
	}
	for _, dir := range fontDirs {
		file := filepath.Join(dir, name)
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
			return file, nil
		}
	}
	return "", fmt.Errorf("未找到字体 %s，请安装 fonts-noto-cjk 或配置字体文件路径", name)
}

// loadFace parses a TTF, OTF or font collection. In a collection it picks the
// face named wantFamily, or the first face when wantFamily is empty.
func loadFace(file, wantFamily string) (face, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return face{}, err
	}
	collection, err := opentype.ParseCollection(data)
	if err != nil {
		return face{}, err
	}
	var b sfnt.Buffer
	for i := range collection.NumFonts() {
		f, err := collection.Font(i)
		if err != nil {
			continue
		}
		if wantFamily != "" {
			if name, err := f.Name(&b, sfnt.NameIDFamily); err != nil || name != wantFamily {
				continue
			}
		}
		upem := fixed.Int26_6(f.UnitsPerEm()) << 6
		m, err := f.Metrics(&b, upem, font.HintingNone)
		if err != nil {
			return face{}, err
		}
		return face{f, float64(f.UnitsPerEm()), -float64(m.Ascent) / 64, float64(m.Descent) / 64}, nil
	}
	return face{}, errors.New("字体文件中没有可用字体：" + file)
}

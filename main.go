package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Konfigurasi warna standar terminal
var terminalColors = map[int]color.Color{
	30: color.RGBA{128, 128, 128, 255}, // Hitam/Abu
	31: color.RGBA{255, 85, 85, 255},   // Merah
	32: color.RGBA{85, 255, 85, 255},   // Hijau
	33: color.RGBA{255, 255, 85, 255},  // Kuning
	34: color.RGBA{85, 85, 255, 255},   // Biru
	35: color.RGBA{255, 85, 255, 255},  // Magenta
	36: color.RGBA{85, 255, 255, 255},  // Cyan (Warna Logo XFILES)
	37: color.White,                    // Putih
	90: color.RGBA{170, 170, 170, 255}, // Abu Terang
	91: color.RGBA{255, 128, 128, 255}, // Merah Terang
	92: color.RGBA{128, 255, 128, 255}, // Hijau Terang
	93: color.RGBA{255, 255, 128, 255}, // Kuning Terang
	94: color.RGBA{128, 128, 255, 255}, // Biru Terang
	95: color.RGBA{255, 128, 255, 255}, // Magenta Terang
	96: color.RGBA{128, 255, 255, 255}, // Cyan Terang
	97: color.White,                    // Putih Terang
}

// coloredSegment adalah segmen teks custom yang mendukung warna foreground/background
type coloredSegment struct {
	widget.TextSegment
	fgColor color.Color
	bgColor color.Color
}

func (s *coloredSegment) Visual() fyne.CanvasObject {
	text := canvas.NewText(s.Text, s.fgColor)
	text.TextStyle = fyne.TextStyle{Monospace: true}
	text.TextSize = 12

	if s.bgColor == nil || s.bgColor == color.Transparent {
		return text
	}

	bg := canvas.NewRectangle(s.bgColor)
	return container.NewStack(bg, text)
}

// terminalHandler menangani stream stdout dan stderr
type terminalHandler struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex
	lastFg   color.Color
	lastBg   color.Color
}

func (h *terminalHandler) Write(p []byte) (n int, err error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	raw := string(p)
	i := 0
	runes := []rune(raw)

	var currentText strings.Builder

	for i < len(runes) {
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			// Flush teks yang terkumpul sebelumnya
			if currentText.Len() > 0 {
				h.appendSegment(currentText.String())
				currentText.Reset()
			}

			// Cari akhir kode ANSI (huruf m, J, H, dll)
			start := i + 2
			j := start
			for j < len(runes) && !((runes[j] >= 'a' && runes[j] <= 'z') || (runes[j] >= 'A' && runes[j] <= 'Z')) {
				j++
			}

			if j < len(runes) {
				code := string(runes[start:j])
				cmd := runes[j]

				if cmd == 'm' {
					// Handle Warna
					codes := strings.Split(code, ";")
					for _, c := range codes {
						val, _ := strconv.Atoi(c)
						if val == 0 {
							h.lastFg = color.White
							h.lastBg = color.Transparent
						} else if val >= 30 && val <= 37 {
							h.lastFg = terminalColors[val]
						} else if val >= 90 && val <= 97 {
							h.lastFg = terminalColors[val]
						} else if val >= 40 && val <= 47 {
							h.lastBg = terminalColors[val-10]
						}
					}
				} else if cmd == 'J' || cmd == 'H' {
					// Clear Screen atau Home - Kita reset tampilan
					h.richText.Segments = nil
				}
				i = j + 1
				continue
			}
		}

		currentText.WriteRune(runes[i])
		i++
	}

	if currentText.Len() > 0 {
		h.appendSegment(currentText.String())
	}

	h.richText.Refresh()
	h.scroll.ScrollToBottom()
	return len(p), nil
}

func (h *terminalHandler) appendSegment(txt string) {
	// Bersihkan karakter sampah yang sering muncul di Android
	txt = strings.ReplaceAll(txt, "\r", "")
	if txt == "" {
		return
	}

	seg := &coloredSegment{
		fgColor: h.lastFg,
		bgColor: h.lastBg,
	}
	seg.Text = txt
	seg.Style = widget.RichTextStyleCodeInline // Memastikan font monospace

	h.richText.Segments = append(h.richText.Segments, seg)
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("XFILES Executor")
	myWindow.Resize(fyne.NewSize(800, 600))

	// Setup Output Area (Terminal)
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapOff // PENTING: Mencegah logo ASCII pecah

	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth // Biarkan user geser jika teks lebar

	// Background hitam solid agar logo terlihat bagus
	bg := canvas.NewRectangle(color.Black)
	terminalContainer := container.NewStack(bg, logScroll)

	handler := &terminalHandler{
		richText: outputRich,
		scroll:   logScroll,
		lastFg:   color.White,
		lastBg:   color.Transparent,
	}

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menjalankan...")
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Pembersihan dan penyiapan file di /data/local/tmp
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
				copyStdin.Close()
			}()
			copyCmd.Run()

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// Mengatur environment agar skrip mengirimkan warna asli
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=100",
				"LINES=30",
			)

			stdinPipe, _ = cmd.StdinPipe()
			cmd.Stdout = handler
			cmd.Stderr = handler

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	btnSend := widget.NewButton("Kirim", func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			inputEntry.SetText("")
		}
	})

	// Layouting UI
	topBox := container.NewVBox(btnSelect, statusLabel)
	bottomBox := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	content := container.NewBorder(topBox, bottomBox, nil, nil, terminalContainer)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}


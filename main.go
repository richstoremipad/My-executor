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

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ansiColors memetakan kode foreground ANSI standar ke warna Fyne.
var ansiColors = map[int]color.Color{
	30: color.RGBA{128, 128, 128, 255}, // Abu-abu
	31: color.RGBA{255, 85, 85, 255},   // Merah
	32: color.RGBA{85, 255, 85, 255},   // Hijau
	33: color.RGBA{255, 255, 85, 255},  // Kuning
	34: color.RGBA{85, 85, 255, 255},   // Biru
	35: color.RGBA{255, 85, 255, 255},  // Magenta
	36: color.RGBA{85, 255, 255, 255},  // Cyan (Warna Logo XFILES)
	37: color.White,                    // Putih
}

// terminalSegment menangani perataan teks dan warna kustom dalam RichText.
type terminalSegment struct {
	widget.TextSegment
	FgColor color.Color
}

func (s *terminalSegment) Visual() fyne.CanvasObject {
	t := canvas.NewText(s.Text, s.FgColor)
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.TextSize = 12
	return t
}

// terminalBuffer mengolah stream output shell dan status terminal.
type terminalBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex
	lastFg   color.Color
}

func (h *terminalBuffer) Write(p []byte) (n int, err error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	raw := string(p)
	runes := []rune(raw)
	var currentText strings.Builder

	for i := 0; i < len(runes); i++ {
		// Deteksi Kode Escape ANSI
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			if currentText.Len() > 0 {
				h.appendSeg(currentText.String())
				currentText.Reset()
			}
			j := i + 2
			// Cari huruf perintah (m, J, H, dll.)
			for j < len(runes) && !((runes[j] >= 'a' && runes[j] <= 'z') || (runes[j] >= 'A' && runes[j] <= 'Z')) {
				j++
			}
			if j < len(runes) {
				code := string(runes[i+2 : j])
				switch runes[j] {
				case 'm': // Kode warna
					parts := strings.Split(code, ";")
					for _, c := range parts {
						val, _ := strconv.Atoi(c)
						if val == 0 {
							h.lastFg = color.White
						} else if col, ok := ansiColors[val]; ok {
							h.lastFg = col
						}
					}
				case 'J', 'H': // Bersihkan Layar atau Cursor Home
					h.richText.Segments = nil 
				}
				i = j
				continue
			}
		}
		currentText.WriteRune(runes[i])
	}
	if currentText.Len() > 0 {
		h.appendSeg(currentText.String())
	}

	h.richText.Refresh()
	h.scroll.ScrollToBottom()
	return len(p), nil
}

func (h *terminalBuffer) appendSeg(txt string) {
	txt = strings.ReplaceAll(txt, "\r", "")
	if txt == "" {
		return
	}
	seg := &terminalSegment{FgColor: h.lastFg}
	seg.Text = txt
	h.richText.Segments = append(h.richText.Segments, seg)
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("XFILES Terminal Executor")
	myWindow.Resize(fyne.NewSize(800, 600))

	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapOff // Mencegah logo ASCII pecah atau melipat

	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth // Memungkinkan scroll horizontal untuk logo lebar

	terminalBG := canvas.NewRectangle(color.Black)
	terminalArea := container.NewStack(terminalBG, logScroll)

	handler := &terminalBuffer{
		richText: outputRich,
		scroll:   logScroll,
		lastFg:   color.White,
	}

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Masukkan perintah di sini...")
	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		outputRich.Segments = nil
		outputRich.Refresh()
		statusLabel.SetText("Status: Berjalan...")

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		go func() {
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			cmd := exec.Command("su", "-c", targetPath)
			if !bytes.HasPrefix(data, []byte("\x7fELF")) {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// Environmental hints agar skrip mendeteksi mode terminal
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=100",
				"LINES=30",
			)

			stdinPipe, _ = cmd.StdinPipe()
			cmd.Stdout = handler
			cmd.Stderr = handler
			_ = cmd.Run()
			statusLabel.SetText("Status: Selesai")
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

	top := container.NewVBox(btnSelect, statusLabel)
	bottom := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	myWindow.SetContent(container.NewBorder(top, bottom, nil, nil, terminalArea))
	myWindow.ShowAndRun()
}


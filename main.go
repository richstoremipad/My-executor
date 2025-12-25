package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
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

// --- KONFIGURASI WARNA & PARSER ---

// Struktur Buffer
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex // Mutex untuk mencegah crash saat refresh cepat
	
	// State
	lastColor color.Color
	lastBold  bool
}

// Map warna ANSI (Foreground)
var ansiColors = map[int]color.Color{
	30: color.RGBA{150, 150, 150, 255}, // Black/Gray
	31: color.RGBA{255, 80, 80, 255},   // Red
	32: color.RGBA{80, 200, 80, 255},   // Green
	33: color.RGBA{255, 255, 80, 255},  // Yellow
	34: color.RGBA{100, 150, 255, 255}, // Blue
	35: color.RGBA{255, 100, 255, 255}, // Magenta
	36: color.RGBA{100, 255, 255, 255}, // Cyan
	37: color.White,                    // White
	90: color.RGBA{100, 100, 100, 255}, // Bright Black
	91: color.RGBA{255, 0, 0, 255},     // Bright Red
	92: color.RGBA{0, 255, 0, 255},     // Bright Green
	93: color.RGBA{255, 255, 0, 255},   // Bright Yellow
	94: color.RGBA{92, 92, 255, 255},   // Bright Blue
	95: color.RGBA{255, 0, 255, 255},   // Bright Magenta
	96: color.RGBA{0, 255, 255, 255},   // Bright Cyan
	97: color.White,                    // Bright White
}

// Segment Teks Kustom (Agar warna lebih tajam & Monospace terjamin)
type coloredText struct {
	widget.TextSegment
	Color color.Color
}

func (t *coloredText) Update(o fyne.CanvasObject) {
	if text, ok := o.(*canvas.Text); ok {
		text.Text = t.Text
		text.Color = t.Color
		text.TextStyle = t.Style.TextStyle
		text.TextSize = 11 // Ukuran font sedikit lebih kecil agar muat banyak
		text.Refresh()
	}
}

func (t *coloredText) Visual() fyne.CanvasObject {
	text := canvas.NewText(t.Text, t.Color)
	text.TextStyle = t.Style.TextStyle
	text.TextStyle.Monospace = true
	text.TextSize = 11
	return text
}

// --- LOGIKA PARSING UTAMA ---

func (cb *customBuffer) appendAnsiText(text string) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	// Regex yang lebih kuat: Menangkap Warna (m), Clear Screen (J), Cursor (H), dll
	// Format: \x1b [ param char
	re := regexp.MustCompile(`\x1b\[([0-9;?]*)([a-zA-Z])`)

	// Split teks berdasarkan kode ANSI
	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	// Tambahkan teks awal sebelum kode pertama
	if len(parts) > 0 && parts[0] != "" {
		cb.addSegmentLocked(parts[0])
	}

	for i, match := range matches {
		paramStr := match[1] // Angka (misal: "1;31" atau "2")
		cmdChar := match[2]  // Perintah (misal: "m", "J", "H")

		switch cmdChar {
		case "m": // --- PERINTAH WARNA ---
			codes := strings.Split(paramStr, ";")
			for _, c := range codes {
				val, _ := strconv.Atoi(c)
				if val == 0 {
					cb.lastColor = color.White
					cb.lastBold = false
				} else if val == 1 {
					cb.lastBold = true
				} else if col, ok := ansiColors[val]; ok {
					cb.lastColor = col
				}
				// Abaikan kode background (40-47) karena RichText Fyne sulit merender background warna per kata
			}

		case "J": // --- PERINTAH CLEAR SCREEN ---
			// Jika script kirim "2J" (Clear Screen), kita hapus segmen teks!
			if paramStr == "2" {
				cb.richText.Segments = nil
			}

		case "H": // --- PERINTAH HOME CURSOR ---
			// Biasanya [H dikirim bersamaan dengan [2J. 
			// Jika berdiri sendiri, biasanya minta overwrite dari atas.
			// Untuk simplifikasi Fyne, kita anggap ini refresh UI jika buffer sudah penuh.
			// (Kita abaikan logic overwrite kompleks, tapi kita cegah print "[H")

		case "l", "h": 
			// Kode hide/show cursor (?25l), abaikan saja agar tidak jadi sampah.
		}

		// Tambahkan teks actual setelah kode ini
		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addSegmentLocked(parts[i+1])
		}
	}

	cb.richText.Refresh()
	// Hanya scroll ke bawah jika tidak baru saja di-clear
	if len(cb.richText.Segments) > 0 {
		cb.scroll.ScrollToBottom()
	}
}

func (cb *customBuffer) addSegmentLocked(text string) {
	col := cb.lastColor
	if col == nil {
		col = color.White
	}

	// Filter karakter aneh yang mungkin tersisa
	text = strings.ReplaceAll(text, "\r", "") // Hapus carriage return agar tidak numpuk
	
	seg := &coloredText{
		TextSegment: widget.TextSegment{
			Text: text,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true, Bold: cb.lastBold},
			},
		},
		Color: col,
	}
	cb.richText.Segments = append(cb.richText.Segments, seg)
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	rawText := string(p)

	// Hapus logic "Expires" jika mengganggu layout, atau biarkan
	// re := regexp.MustCompile(`(?i)Expires:.*`)
	// modifiedText := re.ReplaceAllString(rawText, "Expires: 99 days")
	
	// Kirim langsung ke parser (tanpa modifikasi expires agar layout asli terjaga)
	cb.appendAnsiText(rawText)

	return len(p), nil
}

// --- MAIN UI ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor (Clean UI)")
	myWindow.Resize(fyne.NewSize(700, 600)) // Ukuran diperbesar sedikit

	// Menggunakan background gelap custom (Workaround sederhana: Rect hitam di belakang)
	// Tapi untuk RichText, kita biarkan default theme Fyne (biasanya mengikuti System Mode).
	// Pastikan HP/Emulator dalam "Dark Mode" agar teks putih terlihat.

	outputRich := widget.NewRichText()
	outputRich.Scroll = container.ScrollNone
	
	// Wrapper scroll
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik pilihan (1, 2, dll)...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan...")
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Setup File
			exec.Command("su", "-c", "rm "+targetPath).Run() // Bersihkan file lama
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Running...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// ENV PENTING: Set TERM ke xterm agar script tidak mengirim kode aneh-aneh
			// COLUMNS dan LINES membantu script formatting layout
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm", 
				"COLUMNS=80", 
				"LINES=24",
			)

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{
				richText:  outputRich,
				scroll:    logScroll,
				window:    myWindow,
				lastColor: color.White,
			}

			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Selesai (Exit Code)")
			} else {
				statusLabel.SetText("Selesai (Sukses)")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			inputEntry.SetText("")
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Load Script (BASH/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})
	
	// Layout
	bottomBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	topBar := container.NewVBox(btnSelect, statusLabel)
	
	// Gunakan background Hitam pekat di belakang Log agar seperti terminal
	bgRect := canvas.NewRectangle(color.RGBA{10, 10, 10, 255})
	logContainer := container.NewStack(bgRect, logScroll)

	content := container.NewBorder(
		topBar,
		bottomBar,
		nil, nil,
		logContainer,
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}


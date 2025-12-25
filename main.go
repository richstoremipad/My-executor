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

// --- STRUKTUR DATA ---

// customBuffer tidak lagi membutuhkan 'window'
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex 
	
	// State warna dan style
	lastColor color.Color
	lastBold  bool
}

// Map warna ANSI
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

// Custom Segment untuk render teks warna yang lebih presisi
type coloredText struct {
	widget.TextSegment
	Color color.Color
}

func (t *coloredText) Update(o fyne.CanvasObject) {
	if text, ok := o.(*canvas.Text); ok {
		text.Text = t.Text
		text.Color = t.Color
		text.TextStyle = t.Style.TextStyle
		// Ukuran 11 cukup pas untuk simulasi terminal di mobile
		text.TextSize = 11 
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

// --- LOGIC PARSER (PENANGANI ARTEFAK) ---

func (cb *customBuffer) appendAnsiText(text string) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	// Regex ini menangkap Escape Sequence ANSI:
	// \x1b  : Escape char
	// \[    : Bracket buka
	// [...] : Parameter (angka/titik koma/tanda tanya)
	// [a-zA-Z] : Perintah (m=Warna, J=Clear, H=Home, l=HideCursor, dll)
	re := regexp.MustCompile(`\x1b\[([0-9;?]*)([a-zA-Z])`)

	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	if len(parts) > 0 && parts[0] != "" {
		cb.addSegmentLocked(parts[0])
	}

	for i, match := range matches {
		paramStr := match[1]
		cmdChar := match[2]

		switch cmdChar {
		case "m": // --- Ganti Warna ---
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
			}

		case "J": // --- Clear Screen ---
			// Script biasanya mengirim "2J" untuk clear screen
			// Kita hapus semua segment agar layar bersih (seperti pindah halaman)
			if strings.Contains(paramStr, "2") {
				cb.richText.Segments = nil
			}

		case "H": // --- Cursor Home ---
			// Menggerakkan kursor ke atas kiri.
			// Di Fyne log, kita abaikan saja agar tidak muncul simbol aneh,
			// atau jika dikombinasikan dengan Clear (J), segments sudah dihapus.

		case "l", "h": 
			// Menangani kode ?25l (Hide Cursor) dan ?25h (Show Cursor)
			// Kita abaikan agar tidak muncul sebagai sampah teks di layar.
		}

		// Tambahkan teks setelah kode (jika ada)
		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addSegmentLocked(parts[i+1])
		}
	}

	cb.richText.Refresh()
	// Scroll ke bawah otomatis hanya jika layar tidak baru saja dibersihkan
	if len(cb.richText.Segments) > 0 {
		cb.scroll.ScrollToBottom()
	}
}

func (cb *customBuffer) addSegmentLocked(text string) {
	col := cb.lastColor
	if col == nil {
		col = color.White
	}

	// Bersihkan karakter Carriage Return (\r) agar baris tidak menumpuk aneh
	text = strings.ReplaceAll(text, "\r", "")
	
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
	// Langsung kirim ke parser
	cb.appendAnsiText(string(p))
	return len(p), nil
}

// --- MAIN PROGRAM ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Executor")
	myWindow.Resize(fyne.NewSize(700, 600))

	// 1. Setup Output Area
	outputRich := widget.NewRichText()
	outputRich.Scroll = container.ScrollNone
	logScroll := container.NewScroll(outputRich)

	// 2. Setup Background Hitam (Agar terlihat seperti Terminal asli)
	// Kita gunakan Stack container: Background di bawah, Teks di atas
	bgRect := canvas.NewRectangle(color.RGBA{15, 15, 15, 255}) // Hitam pekat
	logArea := container.NewStack(bgRect, logScroll)

	// 3. Setup Input & Controls
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input menu disini...")
	statusLabel := widget.NewLabel("Status: Menunggu File...")
	
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan...")
		
		// Reset layar sebelum mulai
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Hapus file lama jika ada, lalu copy file baru
			exec.Command("su", "-c", "rm "+targetPath).Run()
			
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Script Berjalan")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// PENTING: Setting ENV agar script mengenali terminal
			// 'xterm-256color' mengaktifkan warna
			// 'COLUMNS' & 'LINES' menjaga layout ASCII art tetap rapi
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=80",
				"LINES=25",
			)

			stdinPipe, _ = cmd.StdinPipe()

			// Inisialisasi Buffer (SUDAH DIPERBAIKI: field 'window' dihapus)
			combinedBuf := &customBuffer{
				richText:  outputRich,
				scroll:    logScroll,
				lastColor: color.White,
			}

			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Script Berhenti (Error/Exit)")
			} else {
				statusLabel.SetText("Script Selesai")
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
	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	// Layout UI
	controls := container.NewVBox(btnSelect, statusLabel)
	inputBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)

	finalLayout := container.NewBorder(
		controls, // Top
		inputBar, // Bottom
		nil, nil,
		logArea,  // Center (Hitam + Teks)
	)

	myWindow.SetContent(finalLayout)
	myWindow.ShowAndRun()
}


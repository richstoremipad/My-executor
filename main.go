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

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Struktur Buffer yang dimodifikasi untuk RichText
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
	// Menyimpan state warna terakhir agar nyambung antar chunk output
	lastColor color.Color
	lastBold  bool
}

// Map warna ANSI standar ke warna Go
var ansiColors = map[int]color.Color{
	30: color.RGBA{100, 100, 100, 255}, // Black (Grayish for visibility)
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

// Fungsi sederhana untuk parsing ANSI ke RichText Segments
func (cb *customBuffer) appendAnsiText(text string) {
	// Regex untuk memisahkan kode ANSI: \x1b[...m
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	
	// Split teks berdasarkan kode ANSI, tapi keep delimiter-nya untuk diproses
	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	// Proses bagian pertama (text sebelum kode warna pertama)
	if len(parts) > 0 && parts[0] != "" {
		cb.addSegment(parts[0])
	}

	for i, match := range matches {
		codeStr := match[1]
		
		// Parsing kode (misal "1;31" -> Bold, Red)
		codes := strings.Split(codeStr, ";")
		for _, c := range codes {
			val, _ := strconv.Atoi(c)
			if val == 0 {
				// Reset
				cb.lastColor = color.White
				cb.lastBold = false
			} else if val == 1 {
				cb.lastBold = true
			} else if col, ok := ansiColors[val]; ok {
				cb.lastColor = col
			}
		}

		// Tambahkan teks setelah kode warna ini (jika ada)
		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addSegment(parts[i+1])
		}
	}

	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
}

func (cb *customBuffer) addSegment(text string) {
	seg := &widget.TextSegment{
		Text: text,
		Style: widget.RichTextStyle{
			ColorName: widget.RichTextStyleColorName("custom"), // Bypass theme color
			Inline:    true,
			TextStyle: fyne.TextStyle{Monospace: true, Bold: cb.lastBold},
		},
	}
	
	// Jika color nil, default ke White
	if cb.lastColor == nil {
		seg.Style.ColorName = widget.RichTextStyleColorForeground
	} else {
		// Custom color injection (sedikit hacky di Fyne v2 tapi works untuk custom render)
		// Karena Fyne RichText lebih suka ThemeColor, kita gunakan Custom renderer logic jika perlu,
		// tapi cara termudah adalah memaksa segmen teks memiliki warna spesifik jika didukung extension,
		// atau setidaknya membedakan 'Error' (Merah) dan 'Success' (Hijau).
		//
		// *CATATAN*: Widget RichText standar Fyne agak ketat soal warna kustom.
		// Untuk simplifikasi di sini, kita gunakan fitur 'ColoredTextSegment' jika ingin warna spesifik,
		// tapi TextSegment standar lebih stabil. Kita coba mapping ke warna TextSegment.
	}

	// Workaround: Karena Fyne RichText standar sulit menerima color.Color raw, 
	// kita buat segment manual.
	cb.richText.Segments = append(cb.richText.Segments, &widget.TextSegment{
		Text: text,
		Style: widget.RichTextStyle{
			Monospace: true,
			TextStyle: fyne.TextStyle{Bold: cb.lastBold, Monospace: true},
		},
	})
	
	// UPDATE WARNA MANUAL:
	// Karena TextSegment struct di atas tidak punya field Color publik yang mudah diakses untuk raw RGBA,
	// kita gunakan pendekatan 'ColoredText' helper dari Fyne jika memungkinkan,
	// atau kita gunakan MarkdownParse jika input simple. 
	// NAMUN, cara paling robust manual adalah mengganti elemen terakhir:
	lastIdx := len(cb.richText.Segments) - 1
	if segment, ok := cb.richText.Segments[lastIdx].(*widget.TextSegment); ok {
		// Kita simpan color state, tapi RichText standar Fyne membatasi warna ke Theme.
		// Agar support warna RAW, kita harus bungkus TextSegment.
		// TAPI: Untuk script shell, biasanya cukup Merah, Hijau, Kuning.
		// Mari kita map ke style Fyne yang ada atau gunakan container kustom?
		// Tidak, kita gunakan properti internal jika memungkinkan, atau kita gunakan extension.
		
		// SOLUSI TERBAIK UNTUK USER: Gunakan `canvas.Text` di dalam Container VBox? 
		// Itu akan berat. Tetap di RichText, tapi kita map warna ke nama style standar kalau bisa,
		// atau biarkan default.
		
		// UNTUK KODE INI BERJALAN: Saya akan menerapkan custom Colored Segment Wrapper
		// karena RichText Fyne v2.4+ agak restriktif soal warna hex bebas.
	}
}

// Custom segment untuk warna bebas
type coloredTextSegment struct {
	widget.TextSegment
	CustomColor color.Color
}

func (t *coloredTextSegment) Visual() fyne.CanvasObject {
	obj := t.TextSegment.Visual().(*fyne.Container).Objects[0].(*canvas.Text)
	obj.Color = t.CustomColor
	return obj
}

// KITA SEDERHANAKAN: 
// Fyne RichText agak kompleks untuk raw color. Kita gunakan 'Markdown' approach atau
// manipulasi logik Write untuk mengubah Label menjadi Container VBox berisi canvas.Text.
// TAPI itu akan lambat untuk log panjang.
// 
// SOLUSI TERBAIK & PERFORMA TINGGI:
// Kita gunakan RichText, tapi kita map ANSI ke 'RichTextStyleInline'. 
// Sayangnya Fyne belum support set warna hex arbitrer di RichText secara native mudah.
//
// JADI, saya akan memberikan implementasi parsing ANSI yang membungkus teks dalam
// widget.NewRichTextFromSegments(...) dengan warna yang mendekati.

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	rawText := string(p)
	
	// Logika replace user tetap dipertahankan
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(rawText, "Expires: 99 days")

	// Panggil parser ANSI
	cb.appendAnsiText(modifiedText)
	
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor (Color Support)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// GANTI: OutputLabel menjadi RichText
	outputRich := widget.NewRichText()
	outputRich.Scroll = container.ScrollNone // Kita handle scroll di luar
	
	// Set background hitam agar warna terminal terlihat jelas (opsional)
	// Tapi Fyne ikut tema sistem.
	
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		
		// Reset log
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// Tambahkan TERM=xterm-256color agar script tahu kita support warna
			cmd.Env = append(os.Environ(), 
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", 
				"TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()
			
			// Init custom buffer dengan warna awal putih
			combinedBuf := &customBuffer{
				richText: outputRich, 
				scroll: logScroll, 
				window: myWindow,
				lastColor: color.White,
			}
			
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				combinedBuf.appendAnsiText("\n\x1b[31m[Error: " + err.Error() + "]\x1b[0m")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			// Manual append input user (warna cyan)
			inputColor := "\x1b[36m> " + inputEntry.Text + "\x1b[0m\n"
			
			// Kita perlu akses ke buffer yang sama, atau parse manual ke RichText
			// Untuk simpel, kita hack sedikit outputRich langsung:
			// (Sebaiknya gunakan function yang sama, tapi ini konteks UI thread)
			// Kita buat 'dummy' buffer sebentar untuk append
			tmpBuf := &customBuffer{richText: outputRich, scroll: logScroll, lastColor: color.White}
			tmpBuf.appendAnsiText(inputColor)
			
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})
	btnClear := widget.NewButton("Bersihkan Log", func() { 
		outputRich.Segments = nil
		outputRich.Refresh()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

// ------- BAGIAN KUNCI: IMPLEMENTASI SEGMENT WARNA -------
// Fyne RichText standar agak sulit untuk warna custom (non-theme).
// Jadi kita memodifikasi appendAnsiText untuk menggunakan segmen yang tepat.

import "fyne.io/fyne/v2/canvas"

// Kita perlu mendefinisikan TextSegment kustom agar bisa menerima warna Raw
type coloredText struct {
	widget.TextSegment
	Color color.Color
}

func (t *coloredText) Update(o fyne.CanvasObject) {
	if text, ok := o.(*canvas.Text); ok {
		text.Text = t.Text
		text.Color = t.Color
		text.TextStyle = t.Style.TextStyle
		text.TextSize = 12 // Ukuran font monospace
	}
}

func (t *coloredText) Visual() fyne.CanvasObject {
	text := canvas.NewText(t.Text, t.Color)
	text.TextStyle = t.Style.TextStyle
	text.TextStyle.Monospace = true
	text.TextSize = 12
	return text
}

// Timpa kembali fungsi appendAnsiText di customBuffer agar menggunakan coloredText
func (cb *customBuffer) appendAnsiText(text string) {
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	if len(parts) > 0 && parts[0] != "" {
		cb.addColoredSegment(parts[0])
	}

	for i, match := range matches {
		codeStr := match[1]
		codes := strings.Split(codeStr, ";")
		for _, c := range codes {
			val, _ := strconv.Atoi(c)
			if val == 0 {
				cb.lastColor = color.White // Reset ke putih (atau hitam tergantung tema)
				cb.lastBold = false
			} else if val == 1 {
				cb.lastBold = true
			} else if col, ok := ansiColors[val]; ok {
				cb.lastColor = col
			}
		}

		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addColoredSegment(parts[i+1])
		}
	}
	
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
}

func (cb *customBuffer) addColoredSegment(text string) {
	// Gunakan warna saat ini, atau fallback ke White/Black text standar
	currentColor := cb.lastColor
	if currentColor == nil {
		currentColor = color.White // Default terminal text color
	}
	
	// Buat segmen kustom
	seg := &coloredText{
		TextSegment: widget.TextSegment{
			Text: text,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true, Bold: cb.lastBold},
			},
		},
		Color: currentColor,
	}
	
	cb.richText.Segments = append(cb.richText.Segments, seg)
}


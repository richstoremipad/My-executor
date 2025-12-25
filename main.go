package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer menggunakan Label agar teks tajam dan tidak memicu keyboard
type customBuffer struct {
	label  *widget.Label
	scroll *container.Scroll
	window fyne.Window
}

// Menghapus kode ANSI agar log bersih di UI
func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))
	
	// Manipulasi visual untuk teks "Expires"
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(cleanText, "Expires: 99 days")

	cb.label.SetText(cb.label.Text + modifiedText)
	
	// Otomatis scroll ke bawah saat log bertambah
	cb.scroll.ScrollToBottom()
	cb.window.Canvas().Refresh(cb.label)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Pro")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Output menggunakan Label (Anti-Keyboard & Sharp)
	outputLabel := widget.NewLabel("")
	outputLabel.TextStyle = fyne.TextStyle{Monospace: true}
	outputLabel.Wrapping = fyne.TextWrapBreak

	logScroll := container.NewScroll(outputLabel)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input/jawaban di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputLabel.SetText("") 

		// Gunakan /data/local/tmp untuk menghindari masalah izin SELinux
		targetPath := "/data/local/tmp/temp_script.sh"
		
		// Baca seluruh isi file script
		data, err := io.ReadAll(reader)
		if err != nil {
			statusLabel.SetText("Status: Gagal membaca file")
			return
		}

		go func() {
			// Langkah 1: Kirim script ke sistem dengan Root
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			
			// Langkah 2: Eksekusi menggunakan 'sh' agar kompatibel dengan Android
			cmd := exec.Command("su", "-c", "sh "+targetPath)
			
			// Set PATH agar perintah seperti 'date' atau 'bc' bisa ditemukan
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", "TERM=dumb")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{label: outputLabel, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			runErr := cmd.Run()
			if runErr != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				outputLabel.SetText(outputLabel.Text + "\n[Proses Berhenti: " + runErr.Error() + "]\n")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputLabel.SetText(outputLabel.Text + "> " + inputEntry.Text + "\n")
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputLabel.SetText("")
	})

	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	controls := container.NewVBox(btnSelect, btnClear, statusLabel)

	myWindow.SetContent(container.NewBorder(
		controls,
		inputContainer, 
		nil, nil, 
		logScroll,
	))

	myWindow.ShowAndRun()
}


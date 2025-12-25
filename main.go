package main

import (
	"bytes"
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

type customBuffer struct {
	label  *widget.Label
	scroll *container.Scroll
	window fyne.Window
}

func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cleanText := cleanAnsi(string(p))
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(cleanText, "Expires: 99 days")

	cb.label.SetText(cb.label.Text + modifiedText)
	cb.scroll.ScrollToBottom()
	cb.window.Canvas().Refresh(cb.label)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor")
	myWindow.Resize(fyne.NewSize(600, 500))

	outputLabel := widget.NewLabel("")
	outputLabel.TextStyle = fyne.TextStyle{Monospace: true}
	outputLabel.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputLabel)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputLabel.SetText("") 

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		// Cek apakah file adalah binary (ELF) atau script teks
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Langkah 1: Kirim dan beri izin eksekusi penuh
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan...")

			// Langkah 2: Logika Eksekusi Otomatis
			var cmd *exec.Cmd
			if isBinary {
				// Jika binary, jalankan langsung
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				// Jika script teks, jalankan dengan sh
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", "TERM=dumb")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{label: outputLabel, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				outputLabel.SetText(outputLabel.Text + "\n[Error: " + err.Error() + "]")
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
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})
	btnClear := widget.NewButton("Bersihkan Log", func() { outputLabel.SetText("") })

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}


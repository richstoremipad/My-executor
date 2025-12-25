package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type customBuffer struct {
	area   *widget.Entry
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

	cb.area.Append(modifiedText)
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Interactive Root Executor (SHC)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Konfigurasi Output Area agar tidak memicu keyboard
	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true}
	
	// Fitur Baru: Membuat log tidak bisa diedit dan tidak memicu keyboard
	outputArea.Disable() 

	// Membungkus outputArea dalam scroll container agar bisa digeser jari
	logScroll := container.NewScroll(outputArea)
	logScroll.SetMinSize(fyne.NewSize(600, 300))

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini lalu tekan Kirim/Enter...")

	statusLabel := widget.NewLabel("Status: Siap")
	
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("")

		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_bin")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()
		os.Chmod(internalPath, 0755)

		go func() {
			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			cmdString := fmt.Sprintf("cd %s && ./%s", filepath.Dir(internalPath), filepath.Base(internalPath))
			cmd := exec.Command("su", "-c", cmdString)
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=dumb")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				outputArea.Append("\n[Proses Keluar: " + err.Error() + "]\n")
			} else {
				statusLabel.SetText("Status: Selesai")
				outputArea.Append("\n[Proses Selesai]\n")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputArea.Append(fmt.Sprintf("> %s\n", inputEntry.Text)) 
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
		outputArea.SetText("")
	})

	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	controls := container.NewVBox(btnSelect, btnClear, statusLabel)

	// Layouting dengan Scroll Container di bagian tengah
	myWindow.SetContent(container.NewBorder(
		controls,
		inputContainer, 
		nil, nil, 
		logScroll,
	))

	myWindow.ShowAndRun()
}


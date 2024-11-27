package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"github.com/a-h/templ"

	tp "github.com/xypwn/gu-epi-autocorrector/templ"
)

var reAuthor = regexp.MustCompile(`__author__ = "[0-9]+, \\w+"`)

func main() {
	component := tp.Index("GU EPI Autocorrector")

	http.Handle("/", templ.Handler(component))
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.Handle("/upload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10_000_000); err != nil {
			fmt.Println(err)
		}

		file, fileHdr, err := r.FormFile("file")
		if err != nil {
			fmt.Println(err)
		}
		var sanitizedOutFileName strings.Builder
		{
			outFileName := strings.TrimSuffix(fileHdr.Filename, ".zip") + "_out.zip"
			for _, r := range outFileName {
				if (r >= 'a' && r <= 'z') ||
					(r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') ||
					r == '_' || r == '.' || r == '-' {
					sanitizedOutFileName.WriteRune(r)
				} else {
					sanitizedOutFileName.WriteRune('_')
				}
			}
		}

		bIn, err := io.ReadAll(file)
		if err != nil {
			fmt.Println(err)
		}

		zipRd, err := zip.NewReader(bytes.NewReader(bIn), int64(len(bIn)))
		if err != nil {
			fmt.Println(err)
		}

		var bOut bytes.Buffer
		zipWr := zip.NewWriter(&bOut)

		for _, f := range zipRd.File {
			if !strings.HasSuffix(f.Name, ".py") {
				continue
			}
			if err := func() error {
				rd, err := f.Open()
				if err != nil {
					return err
				}
				defer rd.Close()

				b, err := io.ReadAll(rd)
				if err != nil {
					return err
				}

				sc := bufio.NewScanner(bytes.NewReader(b))
				hasAuthor := false
				for sc.Scan() {
					if reAuthor.MatchString(sc.Text()) {
						hasAuthor = true
					}
				}
				if err := sc.Err(); err != nil {
					return err
				}
				// TODO: Actually add author string, asking for name and Matrikelnummer
				if !hasAuthor {
					fmt.Println("author line missing in", f.Name)
				}

				wr, err := zipWr.Create(f.Name)
				if err != nil {
					return err
				}

				cmd := exec.Command("python3", "-m", "autopep8", "-a", "-a", "-")
				cmd.Stdin = bytes.NewReader(b)
				cmd.Stdout = wr
				if err := cmd.Run(); err != nil {
					return err
				}

				return nil
			}(); err != nil {
				fmt.Println(err)
			}
		}

		if err := zipWr.Close(); err != nil {
			fmt.Println(err)
		}

		w.Header().Add("Content-Disposition", "attachment; filename="+sanitizedOutFileName.String())

		if _, err := io.Copy(w, &bOut); err != nil {
			fmt.Println(err)
		}
	}))

	fmt.Println("Listening on :3000")
	http.ListenAndServe(":3000", nil)
}

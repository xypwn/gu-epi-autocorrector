package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
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

// Pos is a Position in source code.
type Pos struct {
	File string
	Ln   int
	Col  int
}

type RuffLinterResult struct {
	//Cell        interface{} `json:"cell"`
	Code        string `json:"code"`
	EndLocation struct {
		Column int `json:"column"`
		Row    int `json:"row"`
	} `json:"end_location"`
	Filename string `json:"filename"`
	Fix      *struct {
		Applicability string `json:"applicability"`
		Edits         []struct {
			Content     string `json:"content"`
			EndLocation struct {
				Column int `json:"column"`
				Row    int `json:"row"`
			} `json:"end_location"`
			Location struct {
				Column int `json:"column"`
				Row    int `json:"row"`
			} `json:"location"`
		} `json:"edits"`
		Message string `json:"message"`
	} `json:"fix,omitempty"`
	Location struct {
		Column int `json:"column"`
		Row    int `json:"row"`
	} `json:"location"`
	Message string `json:"message"`
	NoqaRow int    `json:"noqa_row"`
	URL     string `json:"url"`
}

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

		var linterOut bytes.Buffer

		var zipOut bytes.Buffer
		zipWr := zip.NewWriter(&zipOut)

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

				//linterFoundSomething := false
				var linterRes []RuffLinterResult
				{
					linterOut.Reset()
					cmd := exec.Command(
						"python3", "-m", "ruff", "check",
						"--output-format=json",
						"-",
						"--select=F,E,W,N,D,I",
						"--ignore=D211,D213",
						"--fix",
						//"--ignore=E0401",
					)
					cmd.Stdin = bytes.NewReader(b)
					cmd.Stdout = wr
					cmd.Stderr = &linterOut
					if err := cmd.Run(); err != nil {
						if ee, ok := err.(*exec.ExitError); ok &&
							ee.ExitCode() == 1 {
							//linterFoundSomething = true
						} else {
							return err
						}
					}

					if err := json.Unmarshal(linterOut.Bytes(), &linterRes); err != nil {
						return err
					}
				}
				fmt.Println(linterRes)

				return nil
			}(); err != nil {
				fmt.Println(err)
			}
		}

		if err := zipWr.Close(); err != nil {
			fmt.Println(err)
		}

		w.Header().Add("Content-Disposition", "attachment; filename="+sanitizedOutFileName.String())

		if _, err := io.Copy(w, &zipOut); err != nil {
			fmt.Println(err)
		}
	}))

	fmt.Println("Listening on :3000")
	http.ListenAndServe(":3000", nil)
}

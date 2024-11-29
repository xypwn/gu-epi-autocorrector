package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

var reAuthor = regexp.MustCompile(`__author__ = "[0-9]+, \w+"`)

// Location in source code
type Location struct {
	Column int `json:"column"`
	Row    int `json:"row"`
}

type LinterResult struct {
	Code            string
	Location        Location
	EndLocation     Location
	Message         string
	URL             string
	ContextCodeHTML string
}

// Result item returned by ruff's linter
type ruffLinterResult struct {
	//Cell        interface{} `json:"cell"`
	Code        string   `json:"code"`
	Filename    string   `json:"filename"`
	Location    Location `json:"location"`
	EndLocation Location `json:"end_location"`
	Message     string   `json:"message"`
	/*Fix         *struct {
		Applicability string `json:"applicability"`
		Edits         []struct {
			Content     string   `json:"content"`
			Location    Location `json:"location"`
			EndLocation Location `json:"end_location"`
		} `json:"edits"`
		Message string `json:"message"`
	} `json:"fix,omitempty"`*/
	//NoqaRow int    `json:"noqa_row"`
	URL string `json:"url"`
}

// Sanitizes filename for Content-Disposition HTTP header value.
func sanitizeFilename(s string) string {
	var res strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-' {
			res.WriteRune(r)
		} else {
			res.WriteRune('_')
		}
	}
	return res.String()
}

func processZip(out io.Writer, in []byte) (map[string][]LinterResult, error) {
	zipRd, err := zip.NewReader(bytes.NewReader(in), int64(len(in)))
	if err != nil {
		fmt.Println(err)
	}
	linterRes := make(map[string][]LinterResult)
	var fB bytes.Buffer
	var linterOut bytes.Buffer
	zipWr := zip.NewWriter(out)
	defer zipWr.Close()
	processFile := func(f *zip.File) error {
		fB.Reset()
		{
			rd, err := f.Open()
			if err != nil {
				return err
			}
			defer rd.Close()
			if _, err := io.Copy(&fB, rd); err != nil {
				return err
			}
		}

		wr, err := zipWr.CreateHeader(&f.FileHeader)
		if err != nil {
			return err
		}
		if f.Mode().IsRegular() && strings.HasSuffix(f.Name, ".py") {
			var fLintRes []LinterResult

			fLines := strings.Split(fB.String(), "\n")

			{
				var docStrBorder string
				inDocstr := false
				pastDocstr := false
				hasAuthor := false
				for i, ln := range fLines {
					if !pastDocstr {
						if inDocstr {
							if strings.Count(ln, docStrBorder)%2 == 1 {
								pastDocstr = true
								inDocstr = false
							}
						} else {
							if strings.Contains(ln, `"""`) {
								docStrBorder = `"""`
							} else if strings.Contains(ln, `'''`) {
								docStrBorder = `'''`
							}
							cnt := strings.Count(ln, docStrBorder)
							if cnt > 0 {
								if cnt%2 == 0 {
									pastDocstr = true
								} else {
									inDocstr = true
								}
							}
						}
					}
					if strings.HasPrefix(ln, "__author__") {
						hasAuthor = true
						if reAuthor.MatchString(ln) {
							if !pastDocstr {
								fLintRes = append(fLintRes, LinterResult{
									Location:    Location{1, i + 1},
									EndLocation: Location{len(ln), i + 1},
									Message:     "__author__ line before module docstring (should come after)",
								})
							}
						} else {
							fLintRes = append(fLintRes, LinterResult{
								Location:    Location{len("__author__") + 1, i + 1},
								EndLocation: Location{len(ln), i + 1},
								Message:     "invalid __author__ line (expected format __author__ = \"0123456, Lastname\")",
							})
						}
					}
				}
				if !hasAuthor {
					fLintRes = append(fLintRes, LinterResult{
						Location:    Location{1, 1},
						EndLocation: Location{1, 1},
						Message:     "missing __author__ line",
					})
				}
			}

			{
				linterOut.Reset()
				cmd := exec.Command(
					"python3", "-m", "ruff", "check",
					"--output-format=json",
					"-",
					"--select=F,E,W,N,I,D100,D101,D102,D103,D104,D105,D106,D107",
					"--fix",
				)
				cmd.Stdin = bytes.NewReader(fB.Bytes())
				cmd.Stdout = wr
				cmd.Stderr = &linterOut
				if err := cmd.Run(); err != nil {
					if ee, ok := err.(*exec.ExitError); ok &&
						ee.ExitCode() == 1 {
						// Linter found something
					} else {
						return err
					}
				}

				var ruffLintRes []ruffLinterResult
				if err := json.Unmarshal(linterOut.Bytes(), &ruffLintRes); err != nil {
					return err
				}
				for _, lr := range ruffLintRes {
					fLintRes = append(fLintRes, LinterResult{
						Code:        lr.Code,
						Location:    lr.Location,
						EndLocation: lr.EndLocation,
						Message:     lr.Message,
						URL:         lr.URL,
					})
				}
			}

			for i := range fLintRes {
				var contextCodeHTML bytes.Buffer
				{
					var code strings.Builder
					startRow := max(fLintRes[i].Location.Row-5-1, 0)
					endRow := min(fLintRes[i].EndLocation.Row+5, len(fLines))
					for i := startRow; i < endRow; i++ {
						code.WriteString(fLines[i])
						code.WriteRune('\n')
					}

					codeFormatter := html.New(
						html.WithClasses(true),
						html.HighlightLines([][2]int{
							{fLintRes[i].Location.Row - startRow, fLintRes[i].EndLocation.Row - startRow},
						}),
					)

					lex := lexers.Get("python")
					it, err := lex.Tokenise(nil, code.String())
					if err != nil {
						return err
					}
					if err := codeFormatter.Format(&contextCodeHTML, styles.Get("monokai"), it); err != nil {
						return err
					}
				}
				fLintRes[i].ContextCodeHTML = contextCodeHTML.String()
			}

			linterRes[f.Name] = fLintRes
		} else {
			if _, err := wr.Write(fB.Bytes()); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range zipRd.File {
		if err := processFile(f); err != nil {
			return nil, err
		}
	}
	return linterRes, nil
}

func main() {
	title := "GU EPI Autocorrector"

	var htmlCodeFormatterCSS strings.Builder
	{
		style, err := styles.Get("monokai").
			Builder().
			AddEntry(chroma.LineHighlight, chroma.StyleEntry{
				Background: chroma.ParseColour("#661515"),
			}).
			Build()
		if err != nil {
			panic(err)
		}
		htmlCodeFormatter := html.New(html.WithClasses(true))
		if err := htmlCodeFormatter.WriteCSS(&htmlCodeFormatterCSS, style); err != nil {
			panic(err)
		}
	}

	http.Handle("/", templ.Handler(TplHtmlDoc(
		[]templ.Component{TplUploadForm(false)},
		title,
		nil,
	)))
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.Handle("/upload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") ||
			strings.ToUpper(r.Method) != "POST" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		file, fileHdr, err := r.FormFile("file")
		if err != nil {
			fmt.Println(err)
			return
		}

		outFileName := strings.TrimSuffix(fileHdr.Filename, ".zip") + "_out.zip"

		bIn, err := io.ReadAll(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		var bOut bytes.Buffer
		lintRes, err := processZip(&bOut, bIn)
		if err != nil {
			fmt.Println(err)
			return
		}

		foundProblems := slices.ContainsFunc(slices.Collect(maps.Values(lintRes)), func(slc []LinterResult) bool {
			return len(slc) > 0
		})

		if foundProblems {
			if err := TplHtmlDoc(
				[]templ.Component{TplUploadForm(true), TplLintResults(lintRes, foundProblems)},
				title,
				[]string{htmlCodeFormatterCSS.String()},
			).Render(r.Context(), w); err != nil {
				fmt.Println(err)
				return
			}
		} else {
			w.Header().Add("Content-Disposition", "attachment; filename="+sanitizeFilename(outFileName))
			w.Header().Add("Content-Length", strconv.Itoa(bOut.Len()))
			if _, err := w.Write(bOut.Bytes()); err != nil {
				fmt.Println(err)
				return
			}
		}
	}))

	fmt.Println("Listening on :3000")
	http.ListenAndServe(":3000", nil)
}

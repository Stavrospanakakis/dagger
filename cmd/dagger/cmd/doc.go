package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"cuelang.org/go/cue"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.dagger.io/dagger/cmd/dagger/cmd/common"
	"go.dagger.io/dagger/cmd/dagger/logger"
	"go.dagger.io/dagger/compiler"
	"go.dagger.io/dagger/environment"
	"go.dagger.io/dagger/stdlib"
	"golang.org/x/term"
)

const (
	textFormat     = "txt"
	markdownFormat = "md"
	jsonFormat     = "json"
	textPadding    = "    "
)

// types used for json generation

type ValueJSON struct {
	Name        string
	Type        string
	Description string
}

type FieldJSON struct {
	Name        string
	Description string
	Inputs      []ValueJSON
	Outputs     []ValueJSON
}

type PackageJSON struct {
	Name        string
	Description string
	Fields      []FieldJSON
}

var docCmd = &cobra.Command{
	Use:   "doc [PACKAGE | PATH]",
	Short: "document a package",
	Args:  cobra.ExactArgs(1),
	PreRun: func(cmd *cobra.Command, args []string) {
		// Fix Viper bug for duplicate flags:
		// https://github.com/spf13/viper/issues/233
		if err := viper.BindPFlags(cmd.Flags()); err != nil {
			panic(err)
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		lg := logger.New()
		ctx := lg.WithContext(cmd.Context())

		format := viper.GetString("output")
		if format != textFormat &&
			format != markdownFormat &&
			format != jsonFormat {
			lg.Fatal().Msg("output must be either `txt`, `md` or `json`")
		}

		packageName := args[0]

		val, err := loadCode(packageName)
		if err != nil {
			lg.Fatal().Err(err).Msg("cannot compile code")
		}
		PrintDoc(ctx, packageName, val, format)
	},
}

func init() {
	docCmd.Flags().StringP("output", "o", textFormat, "Output format (txt|md)")

	if err := viper.BindPFlags(docCmd.Flags()); err != nil {
		panic(err)
	}
}

func mdEscape(s string) string {
	escape := []string{"|", "<", ">"}
	for _, c := range escape {
		s = strings.ReplaceAll(s, c, `\`+c)
	}
	return s
}

func terminalTrim(msg string) string {
	// If we're not running on a terminal, return the whole string
	size, _, err := term.GetSize(1)
	if err != nil {
		return msg
	}

	// Otherwise, trim to fit half the terminal
	size /= 2
	for utf8.RuneCountInString(msg) > size {
		msg = msg[0:len(msg)-4] + "…"
	}
	return msg
}

func formatLabel(name string, val *compiler.Value) string {
	label := val.Path().String()
	return strings.TrimPrefix(label, name+".")
}

func loadCode(packageName string) (*compiler.Value, error) {
	sources := map[string]fs.FS{
		stdlib.Path: stdlib.FS,
	}

	src, err := compiler.Build(sources, packageName)
	if err != nil {
		return nil, err
	}

	return src, nil
}

// printValuesText (text) formats an array of Values on stdout
func printValuesText(libName string, values []*compiler.Value) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, len(textPadding), ' ', 0)
	fmt.Printf("\n%sInputs:\n", textPadding)
	for _, i := range values {
		docStr := terminalTrim(common.ValueDocString(i))
		fmt.Fprintf(w, "\t\t%s\t%s\t%s\n",
			formatLabel(libName, i), common.FormatValue(i), docStr)
	}
	w.Flush()
}

// printValuesMarkdown (markdown) formats an array of Values on stdout
func printValuesMarkdown(libName string, values []*compiler.Value) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, len(textPadding), ' ', 0)
	fmt.Fprintf(w, "| Name\t| Type\t| Description    \t|\n")
	fmt.Fprintf(w, "| -------------\t|:-------------:\t|:-------------:\t|\n")
	for _, i := range values {
		fmt.Fprintf(w, "|*%s*\t|``%s``\t|%s\t|\n",
			formatLabel(libName, i),
			mdEscape(common.FormatValue(i)),
			mdEscape(common.ValueDocString(i)))
	}
	fmt.Fprintln(w)
	w.Flush()
}

// printValuesJson fills a struct for json output
func valuesToJSON(libName string, values []*compiler.Value) []ValueJSON {
	val := []ValueJSON{}

	for _, i := range values {
		v := ValueJSON{}
		v.Name = formatLabel(libName, i)
		v.Type = common.FormatValue(i)
		v.Description = common.ValueDocString(i)
		val = append(val, v)
	}

	return val
}

func PrintDoc(ctx context.Context, packageName string, val *compiler.Value, format string) {
	lg := log.Ctx(ctx)

	fields, err := val.Fields(cue.Definitions(true))
	if err != nil {
		lg.Fatal().Err(err).Msg("cannot get fields")
	}

	packageJSON := &PackageJSON{}
	// Package Name + Description
	switch format {
	case textFormat:
		fmt.Printf("Package %s\n", packageName)
		fmt.Printf("\n%s\n", common.ValueDocString(val))
	case markdownFormat:
		fmt.Printf("## Package %s\n", mdEscape(packageName))
		comment := common.ValueDocString(val)
		if comment == "-" {
			fmt.Println()
			break
		}
		fmt.Printf("\n%s\n\n", mdEscape(comment))
	case jsonFormat:
		packageJSON.Name = packageName
		comment := common.ValueDocString(val)
		if comment != "" {
			packageJSON.Description = comment
		}
	}

	// Package Fields
	for _, field := range fields {
		fieldJSON := FieldJSON{}

		if !field.Selector.IsDefinition() {
			// not a definition, skipping
			continue
		}

		name := field.Label()
		v := field.Value
		if v.Cue().IncompleteKind() != cue.StructKind {
			// not a struct, skipping
			continue
		}

		// Field Name + Description
		comment := common.ValueDocString(v)
		switch format {
		case textFormat:
			fmt.Printf("\n%s\n\n%s%s\n", name, textPadding, comment)
		case markdownFormat:
			fmt.Printf("### %s\n\n", name)
			if comment != "-" {
				fmt.Printf("%s\n\n", mdEscape(comment))
			}
		case jsonFormat:
			fieldJSON.Name = name
			comment := common.ValueDocString(val)
			if comment != "" {
				fieldJSON.Description = comment
			}
		}

		// Inputs
		inp := environment.ScanInputs(ctx, v)
		switch format {
		case textFormat:
			if len(inp) == 0 {
				fmt.Printf("\n%sInputs: none\n", textPadding)
				break
			}
			printValuesText(name, inp)
		case markdownFormat:
			fmt.Printf("#### %s Inputs\n\n", mdEscape(name))
			if len(inp) == 0 {
				fmt.Printf("_No input._\n\n")
				break
			}
			printValuesMarkdown(name, inp)
		case jsonFormat:
			fieldJSON.Inputs = valuesToJSON(name, inp)
		}

		// Outputs
		out := environment.ScanOutputs(ctx, v)
		switch format {
		case textFormat:
			if len(out) == 0 {
				fmt.Printf("\n%sOutputs: none\n", textPadding)
				break
			}
			printValuesText(name, out)
		case markdownFormat:
			fmt.Printf("#### %s Outputs\n\n", mdEscape(name))
			if len(out) == 0 {
				fmt.Printf("_No output._\n\n")
				break
			}
			printValuesMarkdown(name, out)
		case jsonFormat:
			fieldJSON.Outputs = valuesToJSON(name, out)
			packageJSON.Fields = append(packageJSON.Fields, fieldJSON)
		}
	}

	if format == jsonFormat {
		data, err := json.MarshalIndent(packageJSON, "", "    ")
		if err != nil {
			lg.Fatal().Err(err).Msg("json marshal")
		}
		fmt.Printf("%s\n", data)
	}
}
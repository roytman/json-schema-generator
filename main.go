// Copyright 2021 IBM Corp.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-tools/pkg/genall"

	"fybrik.io/json-schema-generator/pkg/schemas"
)

//go:embed VERSION
var version string

const (
	rootsOption  = "roots"
	outputOption = "output"
)

var (
	roots     []string
	outputDir string
)

func addGenerator(generators genall.Generators, generator genall.Generator) genall.Generators {
	return append(generators, &generator)
}

// RootCmd defines the root cli command
func RootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "json-schema-generator",
		Short:         "Generate JSON schemas from Go structures",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       strings.TrimSpace(version),
		RunE: func(cmd *cobra.Command, args []string) error {
			var generators genall.Generators
			generators = addGenerator(generators, &schemas.Generator{OutputDir: outputDir})
			runtime, err := generators.ForRoots(roots...)
			if err != nil {
				return err
			}
			if runtime.Run() {
				return errors.New("generator failed with errors")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&roots, rootsOption, "r", []string{}, "Paths and go-style path patterns to use as package roots")
	_ = cmd.MarkFlagRequired(rootsOption)
	cmd.Flags().StringVarP(&outputDir, outputOption, "o", "", "Directory to save JSON schema artifact to")
	_ = cmd.MarkFlagRequired(outputOption)
	return cmd
}

func main() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

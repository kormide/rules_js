package gazelle

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bmatcuk/doublestar"
	"github.com/emirpasic/gods/lists/singlylinkedlist"
	"github.com/emirpasic/gods/sets/treeset"
)

var (
	// BUILD file names
	buildFileNames = []string{"BUILD", "BUILD.bazel"}

	// Supported source file extensions
	typescriptSourceExtensions = treeset.NewWithStringComparator(".js", ".mjs", ".ts", ".tsx", ".jsx")
)

const (
	// The filename (with any of the TS extensions) imported when importing a directory
	indexFileName = "index"
)

// GenerateRules extracts build metadata from source files in a directory.
// GenerateRules is called in each directory where an update is requested
// in depth-first post-order.
func (ts *TypeScript) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	cfgs := args.Config.Exts[languageName].(Configs)
	cfg := cfgs[args.Rel]

	// When we return empty, we mean that we don't generate anything, but this
	// still triggers the indexing for all the TypeScript targets in this
	// package.
	if !cfg.GenerationEnabled() {
		return language.GenerateResult{}
	}

	// If this directory has not been declared as a bazel package it will have been
	// including in the parent BUILD file.
	if !isBazelPackage(args.Dir) {
		return language.GenerateResult{}
	}

	// Collect all source files
	sourceFiles, collectErr := collectSourceFiles(cfg, args)
	if collectErr != nil {
		log.Printf("ERROR: %v\n", collectErr)
		return language.GenerateResult{}
	}

	DEBUG("SOURCE(%q): %s", args.Rel, sourceFiles.Values())

	// Divide src vs test files
	libSourceFiles := treeset.NewWithStringComparator()
	testSourceFiles := treeset.NewWithStringComparator()

	for _, f := range sourceFiles.Values() {
		file := f.(string)
		if cfg.IsTestFile(file) {
			testSourceFiles.Add(file)
		} else {
			libSourceFiles.Add(file)
		}
	}

	// Build the GenerateResult with src and test rules
	var result language.GenerateResult

	addProjectRule(
		args,
		cfg.RenderLibraryName(filepath.Base(args.Dir)),
		libSourceFiles,
		&result,
	)

	addProjectRule(
		args,
		cfg.RenderTestsLibraryName(filepath.Base(args.Dir)),
		testSourceFiles,
		&result,
	)

	return result
}

func addProjectRule(args language.GenerateArgs, targetName string, sourceFiles *treeset.Set, result *language.GenerateResult) {
	// If a build already exists check for name-collisions
	if args.File != nil {
		checkCollisionErrors(targetName, args)
	}

	// Generate nothing if there are no source files
	if sourceFiles.Empty() {
		return
	}

	// Collect import statements from source
	importedFiles := treeset.NewWith(importStatementComparator)

	// TODO(jbedard): parse files concurrently
	fileIt := sourceFiles.Iterator()
	for fileIt.Next() {
		filePath := fileIt.Value().(string)
		if isImportingFile(filePath) {
			fileImports, err := parseFile(filepath.Join(args.Dir, filePath))

			if err != nil {
				fmt.Println("Parse Error:", fmt.Errorf("%q: %v", filePath, err))
			} else {
				for _, imprt := range fileImports {
					importedFiles.Add(ImportStatement{
						Path:             imprt.Path,
						SourcePath:       filePath,
						SourceLineNumber: imprt.LineNumber,
					})

					DEBUG("IMPORT(%q): %q", filePath, imprt.Path)
				}
			}
		}
	}

	tsProject := rule.NewRule(tsProjectKind, targetName)
	tsProject.SetAttr("srcs", sourceFiles.Values())
	tsProject.SetPrivateAttr(config.GazelleImportsKey, importedFiles)

	result.Gen = append(result.Gen, tsProject)
	result.Imports = append(result.Imports, importedFiles)
}

// Parse the passed file for import statements
func parseFile(filePath string) ([]FileImportInfo, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	return NewParser().ParseImports(filePath, string(content)), nil
}

// isBazelPackage determines if the directory is a Bazel package by probing for
// the existence of a known BUILD file name.
func isBazelPackage(dir string) bool {
	for _, buildFilename := range buildFileNames {
		path := filepath.Join(dir, buildFilename)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func collectSourceFiles(cfg *TypeScriptConfig, args language.GenerateArgs) (*treeset.Set, error) {
	sourceFiles := treeset.NewWithStringComparator()
	excludedPatterns := cfg.ExcludedPatterns()

	// Source files
	for _, f := range args.RegularFiles {
		if isImportingFile(f) {
			sourceFiles.Add(f)
		}
	}

	// TODO(jbedard): record generated non-source files (args.GenFiles, args.OtherGen, ?)

	// Sub-Directory files
	// Find source files throughout the sub-directories of this BUILD.
	for _, d := range args.Subdirs {
		err := filepath.Walk(
			filepath.Join(args.Dir, d),
			func(filePath string, info os.FileInfo, err error) error {
				// Propagate errors.
				if err != nil {
					return err
				}

				// If we are visiting a directory recurse if it is not a bazel package.
				if info.IsDir() {
					if isBazelPackage(filePath) {
						return filepath.SkipDir
					}

					return nil
				}

				// Excxluded files. Must be done manually on Subdirs unlike
				// the BUILD directory which gazell filters automatically.
				f, _ := filepath.Rel(args.Dir, filePath)
				if excludedPatterns != nil {
					it := excludedPatterns.Iterator()
					for it.Next() {
						excludedPattern := it.Value().(string)
						isExcluded, err := doublestar.Match(excludedPattern, f)
						if err != nil {
							return err
						}
						if isExcluded {
							return nil
						}
					}
				}

				// Otherwise the file is either source or potentially importable
				if isImportingFile(f) {
					sourceFiles.Add(f)
				}

				return nil
			},
		)

		if err != nil {
			log.Printf("ERROR: %v\n", err)
			return nil, err
		}
	}

	return sourceFiles, nil
}

// Check if a target with the same name we are generating alredy exists,
// and if it is of a different kind from the one we are generating. If
// so, we have to throw an error since Gazelle won't generate it correctly.
func checkCollisionErrors(tsProjectTargetName string, args language.GenerateArgs) {
	collisionErrors := singlylinkedlist.New()

	for _, t := range args.File.Rules {
		if t.Name() == tsProjectTargetName && t.Kind() != tsProjectKind {
			fqTarget := label.New("", args.Rel, tsProjectTargetName)
			err := fmt.Errorf("failed to generate target %q of kind %q: "+
				"a target of kind %q with the same name already exists. "+
				"Use the '# gazelle:%s' directive to change the naming convention.",
				fqTarget.String(), tsProjectKind, t.Kind(), LibraryNamingConvention)
			collisionErrors.Add(err)
		}
	}

	if !collisionErrors.Empty() {
		it := collisionErrors.Iterator()
		for it.Next() {
			log.Printf("ERROR: %v\n", it.Value())
		}
		os.Exit(1)
	}
}

// If the file is ts-compatible source code that may contain typescript imports
func isImportingFile(f string) bool {
	// Currently any source files may be parsed as ts and may contain imports
	return typescriptSourceExtensions.Contains(filepath.Ext(f))
}

// Strip extensions off of a path if it can be imported without the extension
func stripImportExtensions(f string) string {
	if !isImportingFile(f) {
		return f
	}

	return f[:len(f)-len(filepath.Ext(f))]
}

// If the file is an index it can be imported with the directory name
func isIndexFile(f string) bool {
	if !isImportingFile(f) {
		return false
	}

	f = filepath.Base(f)
	f = stripImportExtensions(f)

	return f == indexFileName
}

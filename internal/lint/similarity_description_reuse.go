package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"slices"
	"strconv"
)

func reuseRenamedSimilarityDescriptions(
	blocks []*similarityBlock,
	previous similarityScanCache,
	root string,
) error {
	// Reuse records only from a validated scan with the current model/prompt policy.
	// Keep exact source keys: renames still invalidate pairs, vectors, and attestations.
	if !previous.Descriptions || !previous.policyMatches(previous.Descriptions) {
		return nil
	}

	paths := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		paths[block.RelativePath] = true
	}

	candidates := make(map[string]string)

	for _, block := range previous.Blocks {
		if !paths[block.RelativePath] {
			continue
		}

		if fingerprint := similarityRenameFingerprint(block.Content); fingerprint != "" {
			candidates[block.RelativePath+"\x00"+fingerprint] = block.ContentHash
		}
	}

	for _, block := range blocks {
		fingerprint := similarityRenameFingerprint(block.Content)
		if fingerprint == "" {
			continue
		}

		hash := candidates[block.RelativePath+"\x00"+fingerprint]
		if hash == "" || hash == block.ContentHash {
			continue
		}

		if err := copySimilarityDescriptionRecords(root, block, hash); err != nil {
			return err
		}
	}

	return nil
}

func copySimilarityDescriptionRecords(
	root string,
	block *similarityBlock,
	previousHash string,
) error {
	for _, recordKind := range []similarityDescriptionRecordKind{
		similarityDescriptionSignatures, similarityDescriptionDetails,
	} {
		kind, key := similarityDescriptionCacheKey(block.ContentHash, block.IsTest, recordKind)
		if _, found := loadSimilarityDescription(root, key, kind, recordKind); found {
			continue
		}

		_, previousKey := similarityDescriptionCacheKey(previousHash, block.IsTest, recordKind)
		if description, found := loadSimilarityDescription(
			root,
			previousKey,
			kind,
			recordKind,
		); found {
			if err := storeSimilarityDescription(root, key, description); err != nil {
				return err
			}
		}
	}

	return nil
}

func similarityRenameFingerprint(content string) string {
	const header = "package similarity\n"

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "", header+content, 0)
	if err != nil || len(file.Decls) != 1 {
		return ""
	}

	fn, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok {
		return ""
	}

	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}
	// Fragments lack package dependencies. Only resolved local bindings are erased;
	// unresolved identifiers, types, selectors, and literal keys remain exact.
	config := types.Config{Error: func(error) {}}
	_, _ = config.Check("similarity", fset, []*ast.File{file}, info)
	names := similarityRenameNames(fn, info)

	var scan scanner.Scanner
	scan.Init(fset.File(file.Pos()), []byte(header+content), nil, scanner.ScanComments)

	hash := sha256.New()

	for {
		pos, tok, literal := scan.Scan()
		if tok == token.EOF {
			break
		}

		name, renamed := names[pos]
		if tok == token.IDENT && renamed && name != literal {
			literal = name
		} else {
			literal = "source:" + literal
		}

		_, _ = hash.Write([]byte(tok.String() + "\x00" + literal + "\x00"))
	}

	return hex.EncodeToString(hash.Sum(nil))
}

func similarityRenameNames(fn *ast.FuncDecl, info *types.Info) map[token.Pos]string {
	bindings := make(map[types.Object]int)
	names := similarityLiteralKeyNames(fn)

	var privateFunction types.Object
	// The prompt excludes private declaration names, but public functions, methods,
	// and Go entrypoints keep their contract names.
	if fn.Recv == nil && !fn.Name.IsExported() &&
		!slices.Contains([]string{"init", "main"}, fn.Name.Name) {
		privateFunction = info.ObjectOf(fn.Name)
	}

	ast.Inspect(fn, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || names[ident.Pos()] != "" {
			return true
		}

		obj := info.ObjectOf(ident)
		switch obj := obj.(type) {
		case *types.Var:
			if obj.IsField() {
				return true
			}
		case *types.Func:
			if obj != privateFunction {
				return true
			}
		default:
			return true
		}

		binding, found := bindings[obj]
		if !found {
			binding = len(bindings)
			bindings[obj] = binding
		}

		names[ident.Pos()] = "binding:" + strconv.Itoa(binding)

		return true
	})

	return names
}

func similarityLiteralKeyNames(fn *ast.FuncDecl) map[token.Pos]string {
	names := make(map[token.Pos]string)

	ast.Inspect(fn, func(node ast.Node) bool {
		if key, ok := node.(*ast.KeyValueExpr); ok {
			ast.Inspect(key.Key, func(node ast.Node) bool {
				if ident, ok := node.(*ast.Ident); ok {
					names[ident.Pos()] = ident.Name
				}

				return true
			})
		}

		return true
	})

	return names
}

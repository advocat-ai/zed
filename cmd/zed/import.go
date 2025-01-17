package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"strings"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/authzed/authzed-go/v1"
	"github.com/authzed/spicedb/pkg/tuple"
	"github.com/jzelinskie/cobrautil"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/authzed/zed/internal/decode"
	"github.com/authzed/zed/internal/storage"
)

func registerImportCmd(rootCmd *cobra.Command) {
	rootCmd.AddCommand(importCmd)
	importCmd.Flags().Bool("schema", true, "import schema")
	importCmd.Flags().Bool("relationships", true, "import relationships")
	importCmd.Flags().String("schema-definition-prefix", "", "prefix to add to the schema's definition(s) before importing")
}

var importCmd = &cobra.Command{
	Use:   "import <url>",
	Short: "import schema and relationships from a file or url",
	Example: `
	From a gist:
		zed import https://gist.github.com/ecordell/8e3b613a677e3c844742cf24421c08b6

	From a playground link:
		zed import https://play.authzed.com/s/iksdFvCtvnkR/schema

	From pastebin:
		zed import https://pastebin.com/8qU45rVK

	From a devtools instance:
		zed import https://localhost:8443/download

	From a local file (with prefix):
		zed import file:///Users/zed/Downloads/authzed-x7izWU8_2Gw3.yaml

	From a local file (no prefix):
		zed import authzed-x7izWU8_2Gw3.yaml

	Only schema: 
		zed import --relationships=false file:///Users/zed/Downloads/authzed-x7izWU8_2Gw3.yaml

	Only relationships:
		zed import --schema=false file:///Users/zed/Downloads/authzed-x7izWU8_2Gw3.yaml

	With schema definition prefix:
		zed import --schema-definition-prefix=mypermsystem file:///Users/zed/Downloads/authzed-x7izWU8_2Gw3.yaml
`,
	Args: cobra.ExactArgs(1),
	RunE: cobrautil.CommandStack(LogCmdFunc, importCmdFunc),
}

func importCmdFunc(cmd *cobra.Command, args []string) error {
	configStore, secretStore := defaultStorage()
	token, err := storage.DefaultToken(
		cobrautil.MustGetString(cmd, "endpoint"),
		cobrautil.MustGetString(cmd, "token"),
		configStore,
		secretStore,
	)
	if err != nil {
		return err
	}
	log.Trace().Interface("token", token).Send()

	client, err := authzed.NewClient(token.Endpoint, dialOptsFromFlags(cmd, token.APIToken)...)
	if err != nil {
		return err
	}

	u, err := url.Parse(args[0])
	if err != nil {
		return err
	}

	decoder, err := decode.DecoderForURL(u)
	if err != nil {
		return err
	}
	var p decode.SchemaRelationships
	if _, err := decoder(&p); err != nil {
		return err
	}

	prefix, err := determinePrefixForSchema(cobrautil.MustGetString(cmd, "schema-definition-prefix"), client, nil)
	if err != nil {
		return err
	}

	if cobrautil.MustGetBool(cmd, "schema") {
		if err := importSchema(client, p.Schema, prefix); err != nil {
			return err
		}
	}

	if cobrautil.MustGetBool(cmd, "relationships") {
		if err := importRelationships(client, p.Relationships, prefix); err != nil {
			return err
		}
	}

	return err
}

func importSchema(client *authzed.Client, schema string, definitionPrefix string) error {
	log.Info().Msg("importing schema")

	// Recompile the schema with the specified prefix.
	schemaText, err := rewriteSchema(schema, definitionPrefix)
	if err != nil {
		return err
	}

	// Write the recompiled and regenerated schema.
	request := &v1.WriteSchemaRequest{Schema: schemaText}
	log.Trace().Interface("request", request).Str("schema", schemaText).Msg("writing schema")

	if _, err := client.WriteSchema(context.Background(), request); err != nil {
		return err
	}

	return nil
}

func importRelationships(client *authzed.Client, relationships string, definitionPrefix string) error {
	relationshipUpdates := make([]*v1.RelationshipUpdate, 0)
	scanner := bufio.NewScanner(strings.NewReader(relationships))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//") {
			continue
		}
		rel := tuple.ParseRel(line)
		if rel == nil {
			return fmt.Errorf("failed to parse %s as relationship", line)
		}
		log.Trace().Str("line", line).Send()

		// Rewrite the prefix on the references, if any.
		if len(definitionPrefix) > 0 {
			rel.Resource.ObjectType = fmt.Sprintf("%s/%s", definitionPrefix, rel.Resource.ObjectType)
			rel.Subject.Object.ObjectType = fmt.Sprintf("%s/%s", definitionPrefix, rel.Subject.Object.ObjectType)
		}

		relationshipUpdates = append(relationshipUpdates, &v1.RelationshipUpdate{
			Operation:    v1.RelationshipUpdate_OPERATION_TOUCH,
			Relationship: rel,
		})
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	request := &v1.WriteRelationshipsRequest{Updates: relationshipUpdates}
	log.Trace().Interface("request", request).Msg("writing relationships")
	log.Info().Int("count", len(relationshipUpdates)).Msg("importing relationships")

	if _, err := client.WriteRelationships(context.Background(), request); err != nil {
		return err
	}

	return nil
}

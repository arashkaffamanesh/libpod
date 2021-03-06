package main

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"github.com/containers/image/docker"
	"github.com/pkg/errors"
	"github.com/projectatomic/libpod/cmd/podman/formats"
	"github.com/projectatomic/libpod/libpod"
	"github.com/projectatomic/libpod/libpod/common"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const (
	descriptionTruncLength = 44
	maxQueries             = 25
)

var (
	searchFlags = []cli.Flag{
		cli.StringSliceFlag{
			Name:  "filter, f",
			Usage: "filter output based on conditions provided (default [])",
		},
		cli.StringFlag{
			Name:  "format",
			Usage: "change the output format to a Go template",
		},
		cli.IntFlag{
			Name:  "limit",
			Usage: "limit the number of results",
		},
		cli.BoolFlag{
			Name:  "no-trunc",
			Usage: "do not truncate the output",
		},
		cli.StringSliceFlag{
			Name:  "registry",
			Usage: "specific registry to search",
		},
	}
	searchDescription = `
	Search registries for a given image. Can search all the default registries or a specific registry.
	Can limit the number of results, and filter the output based on certain conditions.`
	searchCommand = cli.Command{
		Name:        "search",
		Usage:       "search registry for image",
		Description: searchDescription,
		Flags:       searchFlags,
		Action:      searchCmd,
		ArgsUsage:   "TERM",
	}
)

type searchParams struct {
	Index       string
	Name        string
	Description string
	Stars       int
	Official    string
	Automated   string
}

type searchOpts struct {
	filter  []string
	limit   int
	noTrunc bool
	format  string
}

type searchFilterParams struct {
	stars       int
	isAutomated *bool
	isOfficial  *bool
}

func searchCmd(c *cli.Context) error {
	args := c.Args()
	if len(args) > 1 {
		return errors.Errorf("too many arguments. Requires exactly 1")
	}
	if len(args) == 0 {
		return errors.Errorf("no argument given, requires exactly 1 argument")
	}
	term := args[0]

	if err := validateFlags(c, searchFlags); err != nil {
		return err
	}

	runtime, err := getRuntime(c)
	if err != nil {
		return errors.Wrapf(err, "could not get runtime")
	}
	defer runtime.Shutdown(false)

	format := genSearchFormat(c.String("format"))
	opts := searchOpts{
		format:  format,
		noTrunc: c.Bool("no-trunc"),
		limit:   c.Int("limit"),
		filter:  c.StringSlice("filter"),
	}

	var registries []string
	if len(c.StringSlice("registry")) > 0 {
		registries = c.StringSlice("registry")
	} else {
		registries, err = libpod.GetRegistries()
		if err != nil {
			return errors.Wrapf(err, "error getting registries to search")
		}
	}

	filter, err := parseSearchFilter(&opts)
	if err != nil {
		return err
	}

	return generateSearchOutput(term, registries, opts, *filter)
}

func genSearchFormat(format string) string {
	if format != "" {
		// "\t" from the command line is not being recognized as a tab
		// replacing the string "\t" to a tab character if the user passes in "\t"
		return strings.Replace(format, `\t`, "\t", -1)
	}
	return "table {{.Index}}\t{{.Name}}\t{{.Description}}\t{{.Stars}}\t{{.Official}}\t{{.Automated}}\t"
}

func searchToGeneric(params []searchParams) (genericParams []interface{}) {
	for _, v := range params {
		genericParams = append(genericParams, interface{}(v))
	}
	return genericParams
}

func (s *searchParams) headerMap() map[string]string {
	v := reflect.Indirect(reflect.ValueOf(s))
	values := make(map[string]string, v.NumField())

	for i := 0; i < v.NumField(); i++ {
		key := v.Type().Field(i).Name
		value := key
		values[key] = strings.ToUpper(splitCamelCase(value))
	}
	return values
}

func getSearchOutput(term string, registries []string, opts searchOpts, filter searchFilterParams) ([]searchParams, error) {
	sc := common.GetSystemContext("", "", false)
	// Max number of queries by default is 25
	limit := maxQueries
	if opts.limit != 0 {
		limit = opts.limit
	}

	var paramsArr []searchParams
	for _, reg := range registries {
		results, err := docker.SearchRegistry(context.TODO(), sc, reg, term, limit)
		if err != nil {
			logrus.Errorf("error searching registry %q: %v", reg, err)
			continue
		}
		index := reg
		arr := strings.Split(reg, ".")
		if len(arr) > 2 {
			index = strings.Join(arr[len(arr)-2:], ".")
		}

		// limit is the number of results to output
		// if the total number of results is less than the limit, output all
		// if the limit has been set by the user, output those number of queries
		limit := maxQueries
		if len(results) < limit {
			limit = len(results)
		}
		if opts.limit != 0 && opts.limit < len(results) {
			limit = opts.limit
		}

		for i := 0; i < limit; i++ {
			if len(opts.filter) > 0 {
				// Check whether query matches filters
				if !(matchesAutomatedFilter(filter, results[i]) && matchesOfficialFilter(filter, results[i]) && matchesStarFilter(filter, results[i])) {
					continue
				}
			}
			official := ""
			if results[i].IsOfficial {
				official = "[OK]"
			}
			automated := ""
			if results[i].IsAutomated {
				automated = "[OK]"
			}
			description := strings.Replace(results[i].Description, "\n", " ", -1)
			if len(description) > 44 && !opts.noTrunc {
				description = description[:descriptionTruncLength] + "..."
			}
			name := index + "/" + results[i].Name
			if index == "docker.io" && !strings.Contains(results[i].Name, "/") {
				name = index + "/library/" + results[i].Name
			}
			params := searchParams{
				Index:       index,
				Name:        name,
				Description: description,
				Official:    official,
				Automated:   automated,
				Stars:       results[i].StarCount,
			}
			paramsArr = append(paramsArr, params)
		}
	}
	return paramsArr, nil
}

func generateSearchOutput(term string, registries []string, opts searchOpts, filter searchFilterParams) error {
	searchOutput, err := getSearchOutput(term, registries, opts, filter)
	if err != nil {
		return err
	}
	if len(searchOutput) == 0 {
		return nil
	}
	out := formats.StdoutTemplateArray{Output: searchToGeneric(searchOutput), Template: opts.format, Fields: searchOutput[0].headerMap()}
	return formats.Writer(out).Out()
}

func parseSearchFilter(opts *searchOpts) (*searchFilterParams, error) {
	filterParams := &searchFilterParams{}
	ptrTrue := true
	ptrFalse := false
	for _, filter := range opts.filter {
		arr := strings.Split(filter, "=")
		switch arr[0] {
		case "stars":
			if len(arr) < 2 {
				return nil, errors.Errorf("invalid `stars` filter %q, should be stars=<value>", filter)
			}
			stars, err := strconv.Atoi(arr[1])
			if err != nil {
				return nil, errors.Wrapf(err, "incorrect value type for stars filter")
			}
			filterParams.stars = stars
			break
		case "is-automated":
			if len(arr) == 2 && arr[1] == "false" {
				filterParams.isAutomated = &ptrFalse
			} else {
				filterParams.isAutomated = &ptrTrue
			}
			break
		case "is-official":
			if len(arr) == 2 && arr[1] == "false" {
				filterParams.isOfficial = &ptrFalse
			} else {
				filterParams.isOfficial = &ptrTrue
			}
			break
		default:
			return nil, errors.Errorf("invalid filter type %q", filter)
		}
	}
	return filterParams, nil
}

func matchesStarFilter(filter searchFilterParams, result docker.SearchResult) bool {
	return result.StarCount >= filter.stars
}

func matchesAutomatedFilter(filter searchFilterParams, result docker.SearchResult) bool {
	if filter.isAutomated != nil {
		return result.IsAutomated == *filter.isAutomated
	}
	return true
}

func matchesOfficialFilter(filter searchFilterParams, result docker.SearchResult) bool {
	if filter.isOfficial != nil {
		return result.IsOfficial == *filter.isOfficial
	}
	return true
}

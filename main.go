package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/jinzhu/configor"
	"github.com/k0kubun/pp"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/config"
	"gopkg.in/src-d/go-git.v4/storage/memory"
)

var (
	// debugMode            = false
	verboseMode          = false
	silentMode           = true
	autoReloadMode       = false
	errorOnUnmatchedKeys = false
	vcsTags              []*vcsTag
	lastVersion          string
	cfg                  *Config
)

type Config struct {
	APPName     string    `json:"app-name" yaml:"app-name"`
	DebugMode   bool      `default:"false" json:"debug-mode" yaml:"debug-mode"`
	VerboseMode bool      `default:"false"json:"verbose-mode" yaml:"verbose-mode"`
	SilentMode  bool      `default:"false"  json:"slient-mode" yaml:"slient-mode"`
	Docker      Docker    `json:"docker" yaml:"docker"`
	VCS         VCS       `json:"vcs" yaml:"vcs"`
	CI          CI        `json:"ci" yaml:"ci"`
	Contacts    []Contact `json:"contacts" yaml:"contacts"`
}

type CI struct {
	Travis Travis `json:"travis" yaml:"travis"`
}

type Contact struct {
	Name  string `json:"name" yaml:"name"`
	Email string `json:"email" yaml:"email"`
}

type Travis struct {
	Enabled  bool   `json:"enabled" yaml:"enabled"`
	Template string `json:"template" yaml:"template"`
}

type Docker struct {
	Owner      string           `json:"owner" yaml:"owner"`
	OutputPath string           `default:"./dockerfiles" json:"output-path" yaml:"output-path"`
	Images     map[string]Image `json:"images" yaml:"images"`
}

type VCS struct {
	Name        string   `json:"name" yaml:"name"`
	URLs        []string `json:"urls" yaml:"urls"`
	SkipVersion []string `json:"skip-version" yaml:"skip-version"`
}

type Image struct {
	Name                string `json:"name" yaml:"name"`
	DockerFileTpl       string `json:"dockerfile" yaml:"dockerfile"`
	DockerEntryPointTpl string `json:"docker-entrypoint" yaml:"docker-entrypoint"`
	DockerSyncTpl       string `json:"docker-sync" yaml:"docker-sync"`
	DockerIgnoreTpl     string `json:"dockerignore" yaml:"dockerignore"`
	DockerComposeTpl    string `json:"dockercompose" yaml:"dockercompose"`
	MakefileTpl         string `json:"makefile" yaml:"makefile"`
	ReadmeTpl           string `json:"readme" yaml:"readme"`
}

type vcsTag struct {
	Name string
	Dir  string
}

func isValidVersion(input string) bool {
	for _, version := range cfg.VCS.SkipVersion {
		if version == input {
			return false
		}
	}
	return true
}

func main() {
	// instanciate new config object
	cfg = &Config{}

	// define cli flags
	config := flag.String("config", "x0rzkov.yml", "configuration file")
	flag.BoolVar(&cfg.DebugMode, "debug", false, "debug mode")
	flag.Parse()

	// load config into struct
	cfg, err := loadConfig(*config)
	if err != nil {
		log.Fatalln(err)
	}
	if cfg.DebugMode {
		pp.Println(cfg)
	}

	// fetch remote tags list
	err, tags := getRemoteTags()
	if err != nil {
		log.Fatalln(err)
	}
	if cfg.DebugMode {
		pp.Println("tags: ", tags)
	}

	// clean-up version prefixes
	var vcsTags []*vcsTag
	for _, tag := range tags {
		dir := tag
		if strings.HasPrefix(tag, "v") {
			dir = strings.Replace(tag, "v", "", -1)
		}
		// exclude versions to skip from generation iteration
		if isValidVersion(tag) {
			vcsTags = append(vcsTags, &vcsTag{Name: tag, Dir: dir})
		}
	}

	// get the last version released
	lastVersion = getLastVersion(tags)
	log.Printf("Latest version: %v", lastVersion)
	vcsTags = append(vcsTags, &vcsTag{Name: "v" + lastVersion, Dir: "latest"})
	if cfg.DebugMode {
		pp.Println("vcsTags: ", vcsTags)
	}

	// remove previously generated content
	removeContents(cfg.Docker.OutputPath)

	// create all destination directories based on release founds
	createDirectories(vcsTags)

	// create content for each images
	for dockerImage, dockerData := range cfg.Docker.Images {
		if cfg.DebugMode {
			pp.Println("dockerImage: ", dockerImage)
			pp.Println(dockerData)
		}

		// create content for each versions
		for _, vcsTag := range vcsTags {
			prefixPath := dockerImage
			if dockerImage == "ubuntu" {
				prefixPath = ""
			}
			if cfg.DebugMode {
				pp.Println("prefixPath:", prefixPath)
			}

			// generate Dockerfile
			if err := generateDockerfile(prefixPath, "dockerImageTemplate", dockerData.DockerFileTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate docker-entrypoint.sh
			if err := generateDockerEntrypoint(prefixPath, "entrypointTemplate", dockerData.DockerEntryPointTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate .dockerignore
			if err := generateDockerIgnore(prefixPath, "dockerIgnoreTemplate", dockerData.DockerIgnoreTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate docker-compose.yml
			if err := generateDockerCompose(prefixPath, "dockercomposeTemplate", dockerData.DockerComposeTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate docker-sync.yml
			if err := generateDockerSync(prefixPath, "dockerSyncTemplate", dockerData.DockerSyncTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate Makefile
			if err := generateMakefile(prefixPath, "makefileTemplate", dockerData.MakefileTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

			// generate README.md
			if err := generateReadme(prefixPath, "readmeTemplate", dockerData.ReadmeTpl, vcsTag); err != nil {
				log.Fatalln(err)
			}

		}
	}

	// generate travis file
	generateTravis(vcsTags)
}

func loadConfig(paths ...string) (*Config, error) {
	// load config from paths
	err := configor.New(&configor.Config{
		Debug:                cfg.DebugMode,
		Verbose:              verboseMode,
		AutoReload:           autoReloadMode,
		ErrorOnUnmatchedKeys: errorOnUnmatchedKeys,
		AutoReloadInterval:   time.Minute,
		AutoReloadCallback: func(config interface{}) {
			fmt.Printf("%v changed", config)
		},
	}).Load(cfg, paths...)
	return cfg, err
}

type dockerfileData struct {
	Version    string `json:"version" yaml:"version"`
	Dir        string `json:"dir" yaml:"dir"`
	Filename   string `json:"filename" yaml:"filename"`
	OutputPath string `json:"output-path" yaml:"output-path"`
}

// https://github.com/Luzifer/gen-dockerfile/blob/master/main.go#L85
func generateDockerfile(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	outputPath := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "Dockerfile")
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tDockerfile := template.Must(template.New(tmplName).Parse(string(tmpl)))
	dockerfile, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &dockerfileData{
		Version: vcsTag.Name,
		Dir:     vcsTag.Dir,
	}
	err = tDockerfile.Execute(dockerfile, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

type travisData struct {
	Versions []*vcsTag         `json:"-" yaml:"-"`
	Commands map[string]string `json:"commands" yaml:"commands"`
}

func generateTravis(vcsTag []*vcsTag) error {
	tmpl, err := Asset(".travis.yml")
	if err != nil {
		return err
	}
	tTravisfile := template.Must(template.New("tmplTravis").Parse(string(tmpl)))
	travisfile, err := os.Create(".travis.yml")
	if err != nil {
		if cfg.DebugMode {
			fmt.Println("Error creating the template :", err)
		}
		return err
	}
	dataTravis := &travisData{
		Versions: vcsTag,
	}
	err = tTravisfile.Execute(travisfile, dataTravis)
	if err != nil {
		if cfg.DebugMode {
			fmt.Println("Error creating the template :", err)
		}
		return err
	}
	return nil
}

type dockerEntrypointData struct {
	Shell    string   `default:"!/bin/sh" json:"shell" yaml:"shell"`
	Funcs    []string `json:"functions" yaml:"functions"`
	Commands []string `json:"commands" yaml:"commands"`
}

func generateDockerEntrypoint(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tEntrypoint := template.Must(template.New(tmplName).Parse(string(tmpl)))
	outputPathEntrypoint := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "docker-entrypoint.sh")
	entrypoint, err := os.Create(outputPathEntrypoint)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &dockerEntrypointData{}
	err = tEntrypoint.Execute(entrypoint, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	err = os.Chmod(outputPathEntrypoint, 0755)
	if err != nil {
		return err
	}
	return nil
}

type makefileData struct {
	Version string            `json:"version" yaml:"version"`
	Vars    []string          `json:"variables" yaml:"variables"`
	Targets map[string]string `json:"targets" yaml:"targets"`
}

func generateMakefile(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tMakefile := template.Must(template.New("tmplMakefile").Parse(string(tmpl)))
	outputPathMakefile := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "Makefile")
	makefile, err := os.Create(outputPathMakefile)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &makefileData{}
	err = tMakefile.Execute(makefile, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

type dockerIgnoreData struct {
	Patterns []string `json:"patterns" yaml:"patterns"`
}

func generateDockerIgnore(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tDockerIgnore := template.Must(template.New(tmplName).Parse(string(tmpl)))
	outputPath := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, ".dockerignore")
	dockerIgnore, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &dockerIgnoreData{}
	err = tDockerIgnore.Execute(dockerIgnore, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

type dockerComposeData struct {
	Version string `json:"version" yaml:"version"`
	Base    string `json:"base" yaml:"base"`
	Dir     string `json:"dir" yaml:"dir"`
}

func generateDockerCompose(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tDockerCompose := template.Must(template.New(tmplName).Parse(string(tmpl)))
	outputPath := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "docker-compose.yml")
	dockerCompose, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &dockerComposeData{
		Base:    prefixPath,
		Version: vcsTag.Name,
		Dir:     vcsTag.Dir,
	}
	err = tDockerCompose.Execute(dockerCompose, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

type readmeData struct {
	Version string `json:"version" yaml:"version"`
	Base    string `json:"base" yaml:"base"`
	Dir     string `json:"dir" yaml:"dir"`
}

func generateReadme(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tReadme := template.Must(template.New(tmplName).Parse(string(tmpl)))
	outputPath := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "README.md")
	readme, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &readmeData{
		Base:    prefixPath,
		Version: vcsTag.Name,
	}
	err = tReadme.Execute(readme, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

type dockerSyncData struct {
	Version string `json:"version" yaml:"version"`
	Base    string `json:"base" yaml:"base"`
	Dir     string `json:"dir" yaml:"dir"`
}

func generateDockerSync(prefixPath, tmplName, tmplFile string, vcsTag *vcsTag) error {
	tmpl, err := Asset(tmplFile)
	if err != nil {
		return err
	}
	tDockerSync := template.Must(template.New(tmplName).Parse(string(tmpl)))
	outputPath := filepath.Join(cfg.Docker.OutputPath, vcsTag.Dir, prefixPath, "docker-sync.yml")
	dockerSync, err := os.Create(outputPath)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	cfg := &dockerSyncData{
		Base:    prefixPath,
		Version: vcsTag.Name,
	}
	err = tDockerSync.Execute(dockerSync, cfg)
	if err != nil {
		fmt.Println("Error creating the template :", err)
		return err
	}
	return nil
}

func getLastVersion(tags []string) string {
	versions := make([]*version.Version, len(tags))
	for i, raw := range tags {
		v, _ := version.NewVersion(raw)
		versions[i] = v
	}
	// After this, the versions are properly sorted
	sort.Sort(version.Collection(versions))
	return versions[len(versions)-1].String()
}

func createDirectories(tags []*vcsTag) {
	for _, tag := range tags {
		for image, _ := range cfg.Docker.Images {
			if image != "ubuntu" {
				os.MkdirAll(path.Join(cfg.Docker.OutputPath, tag.Dir, image), 0755)
			}
		}
	}
}

func removeContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func getRemoteTags() (error, []string) {
	// Create the remote with repository URL
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: cfg.VCS.Name,
		URLs: cfg.VCS.URLs,
	})
	log.Print("Fetching tags...")
	// We can then use every Remote functions to retrieve wanted information
	refs, err := rem.List(&git.ListOptions{})
	if err != nil {
		return err, []string{}
	}
	// Filters the references list and only keeps tags
	var tags []string
	for _, ref := range refs {
		if ref.Name().IsTag() {
			tags = append(tags, ref.Name().Short())
		}
	}
	return nil, tags
}

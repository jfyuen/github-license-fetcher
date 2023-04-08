package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/v51/github"
	"github.com/google/licensecheck"
	"golang.org/x/oauth2"
)

// isStringBlank checks if a string is valid
func isStringBlank(s *string) bool {
	return s == nil || strings.TrimSpace(*s) == ""
}

// PackageSummary contains information to declare third party dependencies
type PackageSummary struct {
	Name           string  `json:"name"`
	Version        string  `json:"version"`
	Lic            *string `json:"lic"`
	PrintedName    *string `json:"printedName"`
	Homepage       *string `json:"homepage"`
	Repository     *string `json:"repository"`
	CopyrightLine  *string `json:"copyright_line"`
	CopyrightURL   *string `json:"copyright_url"`
	Author         *string `json:"author"`
	LicenseURL     *string `json:"license_url"`
	LicenseType    *string `json:"license_type"`
	LicenseTxt     *string `json:"license_txt"`
	licenseContent *string
	Note           *string `json:"note"`
	OriginFileName *string `json:"_fileName"`
	Source         *string `json:"_source"`
	FoundLicense   *string `json:"_license"`
}

func (p PackageSummary) String() string {
	return github.Stringify(p)
}

// fromGithubRepository is a shorthant to create a PackageSummary from a github repository with basic information
func fromGithubRepository(repository *github.Repository, repoURLAtVersion string, version string) PackageSummary {
	packageSummary := PackageSummary{Repository: &repoURLAtVersion, Version: version}

	var license *string
	if repository.License != nil && !isStringBlank(repository.License.SPDXID) && *repository.License.SPDXID != "NOASSERTION" { // keep NOASSERTION as nil
		license = repository.License.SPDXID
	}
	if !isStringBlank(license) && isStringBlank(packageSummary.LicenseType) {
		packageSummary.LicenseType = license
	}
	if !isStringBlank(repository.Homepage) && isStringBlank(packageSummary.Homepage) {
		packageSummary.Homepage = repository.Homepage
	}
	return packageSummary
}

// decodeRepositoryContent returns the decoded bytes from a github file
func decodeRepositoryContent(repoContent *github.RepositoryContent) ([]byte, error) {
	if repoContent == nil {
		return nil, nil
	}

	contentByte, err := base64.StdEncoding.DecodeString(*repoContent.Content)
	if err != nil {
		return nil, fmt.Errorf("could not decode license content: %w", err)
	}
	return contentByte, nil
}

// GetReadme fetches readme found by github for a repository
func GetReadme(client *github.Client, ctx context.Context, owner, repo, tag string) (*github.RepositoryContent, error) {
	readme, _, err := client.Repositories.GetReadme(ctx, owner, repo, &github.RepositoryContentGetOptions{Ref: tag})
	if err != nil {
		return nil, fmt.Errorf("could not get repository (%s/%s) readme at %s: %w", owner, repo, tag, err)
	}
	if readme == nil {
		return nil, fmt.Errorf("no readme file found for repository (%s, %s)  for tag %s", owner, repo, tag)
	}

	return readme, nil
}

// GetLicense fetches license found by github for a repository
func GetLicense(client *github.Client, ctx context.Context, owner, repo string) (*github.RepositoryLicense, error) {
	license, _, err := client.Repositories.License(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("could not get repository (%s/%s) license: %w", owner, repo, err)
	}

	return license, nil
}

// hasTag checks if the specified tag exists in the repository
func hasTag(client *github.Client, ctx context.Context, owner, repo, tag string) (*string, error) {
	hasTags := true
	page := 0
	for hasTags {
		tags, _, err := client.Repositories.ListTags(ctx, owner, repo, &github.ListOptions{Page: page, PerPage: 100})

		if err != nil && page == 0 {
			return nil, fmt.Errorf("could not list tags from repository (%s/%s) license: %w", owner, repo, err)
		}
		if len(tags) < 100 {
			hasTags = false
		}
		for _, foundTag := range tags {
			if foundTag.Name == nil {
				continue
			}
			if *foundTag.Name == tag || *foundTag.Name == "v"+tag || *foundTag.Name == "rel/"+tag {
				return foundTag.Name, nil
			}
		}
		page += 1
	}
	return nil, nil
}

// isGitHubURL checks if the url really targets github
func isGitHubURL(input string) bool {
	u, err := url.Parse(input)
	if err != nil {
		return false
	}
	host := u.Host
	if strings.Contains(host, ":") {
		host, _, err = net.SplitHostPort(host)
		if err != nil {
			return false
		}
	}
	return host == "github.com"
}

// getOwnerRepository extracts owner and repository from a github URL of the form https://www.github.comt/dataiku/dip
func getOwnerRepository(url string) (string, string, error) {
	if !isGitHubURL(url) {
		return "", "", fmt.Errorf("%s is not a github url", url)
	}

	url = strings.TrimSuffix(url, ".git") // remove .git at the end if exists
	split := strings.Split(url, "/")
	trimmed := make([]string, 0)
	for _, s := range split {
		if !isStringBlank(&s) {
			if s == "tree" { // repository at a given commit, https://github.com/owner/repo/tree/X.Y.Z, so first par is complete
				break
			}
			trimmed = append(trimmed, strings.TrimSpace(s))
		}
	}
	if len(trimmed) != 4 { // https: (empty) github.com owner repository
		return "", "", fmt.Errorf("%s does not appear to be a valid repository url", url)
	}
	return trimmed[2], trimmed[3], nil
}

// getLicenseForVersion fetches the license file found on github for the specified tag
func getLicenseForVersion(client *github.Client, ctx context.Context, owner, repo, tag, packageName string, directoryContent []*github.RepositoryContent, packageSummary *PackageSummary) error {
	license, _ := GetLicense(client, ctx, owner, repo)

	licensesPath := make([]string, 0)
	if license != nil {
		licensesPath = append(licensesPath, *license.Path)
	}
	for _, repoContent := range directoryContent {
		if repoContent == nil {
			continue
		}
		if strings.HasPrefix(strings.ToLower(*repoContent.Name), "license") && (license == nil || *repoContent.Path != *license.Path) {
			licensesPath = append(licensesPath, *repoContent.Path)
		}
	}

	if len(licensesPath) > 1 {
		log.Printf("found %d potential license files for repository (%s,%s) at %s: %v", len(licensesPath), owner, repo, tag, licensesPath)
	}

	var licenseContent *github.RepositoryContent
	for _, licensePath := range licensesPath {
		log.Printf("trying license: %s", licensePath)
		// first try with given tag or hash
		content, _, _, err := client.Repositories.GetContents(ctx, owner, repo, licensePath, &github.RepositoryContentGetOptions{Ref: tag})
		if err == nil && content != nil { // license found
			licenseContent = content
			break
		}
	}
	if licenseContent == nil {
		return fmt.Errorf("could not find a license for repository (%s/%s) at version %s", owner, repo, tag)
	}
	content, err := decodeRepositoryContent(licenseContent)
	if err != nil {
		return err
	}
	if content == nil {
		return nil
	}

	if packageName == "" {
		packageName = fmt.Sprintf("%s-%s", owner, repo)
	}
	fileName := fmt.Sprintf("%s_%s.txt", strings.TrimPrefix(strings.ReplaceAll(packageName, "/", "-"), "@"), packageSummary.Version)
	packageSummary.LicenseURL = licenseContent.HTMLURL
	packageSummary.LicenseTxt = &fileName
	strContent := string(content)
	packageSummary.licenseContent = &strContent

	// still no license type found, try guessing it from its content
	if isStringBlank(packageSummary.LicenseType) {
		cov := licensecheck.Scan(content)
		if len(cov.Match) > 0 {
			lic := cov.Match[0].ID + " (guessed)"
			packageSummary.LicenseType = &lic
		}
	}
	return nil
}

// writeLicenseFile writes the licenseCotent to LicenseTxt
func writeLicenseFile(packageSummary PackageSummary) error {
	if packageSummary.LicenseTxt == nil || packageSummary.licenseContent == nil {
		return nil
	}

	if _, err := os.Stat(*packageSummary.LicenseTxt); err == nil {
		log.Printf("%s already exists, skipping writing file", *packageSummary.LicenseTxt)
	} else {
		err = os.WriteFile(*packageSummary.LicenseTxt, []byte(*packageSummary.licenseContent), 0644)
		if err != nil {
			return fmt.Errorf("could not write license file %s: %w", *packageSummary.LicenseTxt, err)
		}
	}
	return nil
}

func createGithubClient(ctx context.Context) *github.Client {
	if token, ok := os.LookupEnv("GITHUB_TOKEN"); ok {
		log.Println("token found, will use authenticated HTTP client")
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		tc := oauth2.NewClient(ctx, ts)
		return github.NewClient(tc)
	}
	log.Println("no authentication token found, will use unauthenticated HTTP client")
	return github.NewClient(nil)
}

// tryToFindVersion finds if the tag or a matching one exists in the repository
func tryToFindVersion(client *github.Client, ctx context.Context, owner, repo, tag string) (*string, []*github.RepositoryContent, error) {
	_, directoryContent, _, err := client.Repositories.GetContents(ctx, owner, repo, "/", &github.RepositoryContentGetOptions{Ref: tag})
	if err == nil && len(directoryContent) > 0 {
		return &tag, nil, nil
	}

	foundTag, err := hasTag(client, ctx, owner, repo, tag)
	if err != nil {
		return nil, nil, err
	}
	if foundTag == nil {
		return nil, nil, nil
	}

	_, directoryContent, _, err = client.Repositories.GetContents(ctx, owner, repo, "/", &github.RepositoryContentGetOptions{Ref: *foundTag})
	if err != nil {
		return nil, nil, err
	}
	if len(directoryContent) == 0 {
		return nil, nil, nil
	}
	return foundTag, directoryContent, nil
}

func findCopyrightInContent(content string) *string {
	lines := make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		lineCmp := strings.ToLower(strings.TrimSpace(line))
		// Only check if line starts with copyright to prevent other mention in the text
		if strings.HasPrefix(lineCmp, "copyright") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		merged := strings.Join(lines, "\n")
		return &merged
	}
	return nil
}

func searchCopyright(client *github.Client, ctx context.Context, owner, repo, tag string, packageSummary *PackageSummary) error {
	if packageSummary.licenseContent == nil { // no license
		if readme, err := GetReadme(client, ctx, owner, repo, tag); err != nil {
			return err
		} else if content, err := decodeRepositoryContent(readme); err != nil {
			return err
		} else if content != nil {
			strContent := string(content)
			copyrightLine := findCopyrightInContent(strContent)
			if copyrightLine != nil {
				packageSummary.CopyrightLine = copyrightLine
				packageSummary.CopyrightURL = readme.HTMLURL
			}
		}
	} else {
		copyrightLine := findCopyrightInContent(*packageSummary.licenseContent)
		if copyrightLine != nil {
			packageSummary.CopyrightLine = copyrightLine
			packageSummary.CopyrightURL = packageSummary.LicenseURL
		}
	}
	return nil
}

func processSingleRepository(client *github.Client, ctx context.Context, packageName, repositoryURL, tag string) (PackageSummary, error) {
	log.Printf("processing %s@%s", repositoryURL, tag)
	owner, repo, err := getOwnerRepository(repositoryURL)
	if err != nil {
		return PackageSummary{}, err
	}

	repository, _, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return PackageSummary{}, fmt.Errorf("could not get repository for (%s,%s) at %s: %w", owner, repo, tag, err)
	}

	foundTag, directoryContent, err := tryToFindVersion(client, ctx, owner, repo, tag)
	if err != nil {
		return PackageSummary{}, fmt.Errorf("could not get repository(%s,%s) for version %v: %w", owner, repo, tag, err)
	}
	if foundTag == nil {
		return PackageSummary{}, fmt.Errorf("tag %v not found at repository (%s,%s)", tag, owner, repo)
	}

	urlAtVersion := fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repo, *foundTag)
	packageSummary := fromGithubRepository(repository, urlAtVersion, tag)
	if err := getLicenseForVersion(client, ctx, owner, repo, *foundTag, packageName, directoryContent, &packageSummary); err != nil {
		log.Printf("could not retrieve license for %s at %s: %v", repositoryURL, *foundTag, err)
	}

	if err := searchCopyright(client, ctx, owner, repo, *foundTag, &packageSummary); err != nil {
		log.Println(err) // ok, just log error
	}

	if err := writeLicenseFile(packageSummary); err != nil {
		log.Printf("could not write license file for %s at %s: %v", repositoryURL, *foundTag, err)
	}
	return packageSummary, nil
}

func processBatch(packageSummaries []PackageSummary, waitDelay int) {
	ctx := context.Background()
	client := createGithubClient(ctx)
	log.Printf("found %d packages, processing...", len(packageSummaries))
	rateLimitReached := false
	var rateLimitErr *github.RateLimitError
	for i, packageSummary := range packageSummaries {
		log.Printf("checking %s@%s (%d/%d)", packageSummary.Name, packageSummary.Version, i+1, len(packageSummaries))
		if rateLimitReached {
			continue
		}
		if isStringBlank(packageSummary.Repository) {
			log.Printf("no repository found for %s@%s, skipping...", packageSummary.Name, packageSummary.Version)
			continue
		}
		if !isStringBlank(packageSummary.Lic) {
			log.Printf("multiple licenses exist for %s@%s, skipping for safety...", packageSummary.Name, packageSummary.Version)
			continue
		}
		if isLicenseTxtFilled(packageSummary.LicenseTxt) && !isStringBlank(packageSummary.CopyrightLine) {
			log.Printf("a license text and a copyright exist for %s@%s, skipping...", packageSummary.Name, packageSummary.Version)
			continue
		}

		retrieved, err := processSingleRepository(client, ctx, packageSummary.Name, *packageSummary.Repository, packageSummary.Version)
		if err != nil {
			if ok := errors.As(err, &rateLimitErr); ok {
				rateLimitReached = true
				log.Printf("rate limit exceeded, will not query other packages")
			}
			log.Printf("could not process %s@%s: %v", packageSummary.Name, packageSummary.Version, err)
			continue
		}

		packageSummary.Repository = retrieved.Repository
		if isStringBlank(packageSummary.Homepage) {
			packageSummary.Homepage = retrieved.Homepage
		}
		// if a previous license was found for this package, do not overwrite it
		if isStringBlank(packageSummary.LicenseType) {
			packageSummary.LicenseType = retrieved.LicenseType
		}
		if !isStringBlank(retrieved.licenseContent) {
			packageSummary.licenseContent = retrieved.licenseContent
			packageSummary.LicenseTxt = retrieved.LicenseTxt
			packageSummary.LicenseURL = retrieved.LicenseURL
		}
		if isStringBlank(packageSummary.CopyrightLine) && !isStringBlank(retrieved.CopyrightLine) {
			packageSummary.CopyrightLine = retrieved.CopyrightLine
			packageSummary.CopyrightURL = retrieved.CopyrightURL
		}
		packageSummaries[i] = packageSummary               // updated merged version
		time.Sleep(time.Duration(waitDelay) * time.Second) // because of rate limit, sleep a bit if queries to a single repository was done
	}
}

func writeBatchOutputFile(packageSummaries []PackageSummary, baseFile string) error {
	outFileName := fmt.Sprintf("%s.%s", filepath.Base(baseFile), time.Now().Format(time.RFC3339))
	if _, err := os.Stat(outFileName); err == nil {
		return fmt.Errorf("file %s already exists", outFileName)
	}
	f, err := os.Create(outFileName)
	if err != nil {
		return fmt.Errorf("could not create %s: %w", outFileName, err)
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(packageSummaries); err != nil {
		return err
	}
	log.Printf("packages written to %s", outFileName)
	return nil
}

func isLicenseTxtFilled(licenseTxt *string) bool {
	return !isStringBlank(licenseTxt) && !strings.HasPrefix(*licenseTxt, "TODO")
}

func downloadLicenseFile(packageSummary *PackageSummary) error {
	url := *packageSummary.LicenseURL
	if strings.Contains(url, "bitbucket.org") {
		return fmt.Errorf("license URL is on bitbucket.org for %s@%s: skipping as it requires auth", packageSummary.Name, packageSummary.Version)
	}

	if strings.Contains(url, "github.com") {
		url = strings.Replace(url, "github.com", "raw.githubusercontent.com", 1)
		url = strings.Replace(url, "www.", "", 1)
		url = strings.Replace(url, "/tree/", "/", 1)
		url = strings.Replace(url, "/blob/", "/", 1)
	}

	log.Printf("downloading %s for %s@%s", url, packageSummary.Name, packageSummary.Version)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("cannot download license file from %s: %w", url, err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("cannot download license file from %s: received status code %d", url, resp.StatusCode)
	}

	fileName := fmt.Sprintf("%s_%s.txt", strings.TrimPrefix(strings.ReplaceAll(packageSummary.Name, "/", "-"), "@"), packageSummary.Version)

	out, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("cannot create license file %s: %w", fileName, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("cannot write license file %s: %w", fileName, err)
	}
	packageSummary.LicenseTxt = &fileName
	return nil
}

func downloadLicensesBatch(packageSummaries []PackageSummary, waitDelay int) {
	for i, packageSummary := range packageSummaries {
		if isStringBlank(packageSummary.LicenseURL) {
			log.Printf("no license URL for %s@%s: skipping", packageSummary.Name, packageSummary.Version)
			continue
		}
		if isLicenseTxtFilled(packageSummary.LicenseTxt) {
			log.Printf("license text already exist for %s@%s: skipping", packageSummary.Name, packageSummary.Version)
			continue
		}

		if err := downloadLicenseFile(&packageSummary); err != nil {
			log.Print(err)
			continue
		}

		packageSummaries[i] = packageSummary               // updated merged version
		time.Sleep(time.Duration(waitDelay) * time.Second) // because of rate limit, sleep a bit if queries to a single repository was done
	}
}

func main() {
	repoURL := flag.String("r", "", "repository URL, go with tag")
	tag := flag.String("t", "", "tag to check, go with repository URL")
	jsonFile := flag.String("f", "", "file to check, single usage")
	downloadLicense := flag.Bool("l", false, "only download licenses (not restricted to github.com), use with -f")
	waitDelay := flag.Int("d", 2, "delay between 2 packages processing (not the time between 2 Github requests)")
	flag.Parse()

	if *jsonFile != "" {
		log.Printf("loading %s for processing", *jsonFile)
		content, err := os.ReadFile(*jsonFile)
		if err != nil {
			log.Fatal("error when opening file: ", err)
		}

		var packageSummaries []PackageSummary
		err = json.Unmarshal(content, &packageSummaries)
		if err != nil {
			log.Fatal("error when reading file: ", err)
		}
		if *downloadLicense {
			downloadLicensesBatch(packageSummaries, *waitDelay)
		} else {
			processBatch(packageSummaries, *waitDelay)
		}
		if err := writeBatchOutputFile(packageSummaries, *jsonFile); err != nil {
			log.Fatal(err)
		}

		return
	}

	if *repoURL != "" && *tag != "" {
		ctx := context.Background()
		client := createGithubClient(ctx)
		packageSummary, err := processSingleRepository(client, ctx, "", *repoURL, *tag)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("%v", packageSummary)
		return
	}

	log.Fatal("Wrong arguments, expected one of jsonFile or repository URL + tag")
}

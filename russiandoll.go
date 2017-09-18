package main

import (
	"os"
	"log"
	"strings"
	"bytes"
	"os/exec"
	"fmt"
	"net/http"
)


var relationset = []string{}

type Relation struct {
	Parent, Child string

}

var relationfile *os.File
var directory string

func InitFile (name string ) {

	var err error
	relationfile, err = os.Create("" + name +"-relation.txt")
	if err != nil {
		log.Fatal("Cannot create file", err)
	}

	relationfile.WriteString("digraph G {" + "\n")
}

func FinishFile() {
    fmt.Println("FinishFile()")

	relationset = removeDuplicates(relationset)

	for v := range relationset {
		relationfile.WriteString(relationset[v])
	}
	// Return the new slice.

	relationfile.WriteString("}")
	relationfile.Sync()
	relationfile.Close()

}

func showUsage() {

	fmt.Print( "Usage: " + os.Args[0] + " <path to oet file>\n")
	fmt.Print( "       " + os.Args[0] + " --dir <path to directory>\n")

}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hi there, I love %s!", r.URL.Path[1:])


	allfileslist := findAllOETfiles( directory)
	results := strings.Split(allfileslist, "\n")

	for i:= range results {
		aFile := results[i]
		aFile = strings.TrimSpace(aFile)
		if strings.HasSuffix( aFile,".oet" )  {
			fmt.Fprintf(w, aFile + "<br>")

		}
	}

}


func main() {
	//var file = os.Args[1]



	var filepath string

	if len(os.Args) == 1 {
		showUsage()
		return
	}

	filepath = os.Args[1]
	//filepath = os.Args[2]

	if filepath == ""  {
		showUsage()
		return
	}

	InitFile(filepath)
	log.Printf("args = " + filepath)

	if os.Args[1] == "--directory" {

		if len(os.Args) == 2 {
			showUsage()
			return
		}

		directory = os.Args[2]
		fmt.Print( "mapping directory " + directory + "\n")
		files := findAllOETfiles( directory)
		processAllOETfiles(files)

	} else {
		template_id := FindRootTemplateID( filepath)
		log.Printf(template_id)
		FindParentTemplates(template_id, filepath)
	}

	// call grep function

	http.HandleFunc("/", handler)
	http.ListenAndServe(":8080", nil)


	FinishFile()

}

func FindRootTemplateID( path string) string {

	log.Printf( "FindRootTemplate - " + path)

	var _result = GrepFile( path, "<id>")
	log.Printf("result " + _result)

	r := strings.NewReplacer("<id>", "", "</id>", "")
	template_id := "test"
	template_id = r.Replace(_result)
	template_id = strings.TrimSpace(template_id)

	return template_id
}

func  GrepDir( pattern string ) string {

	cmd := exec.Command("grep", "-r", "template_id=\"" + pattern)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf
//	log.Printf("Running command and waiting for it to finish...")
	err := cmd.Run()
	log.Printf("Command finished with error: %v", err)
	stdout := outbuf.String()
	return stdout
}

func GrepFile( file string, pattern string ) string {

	cmd := exec.Command("grep", pattern,  file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

//	log.Printf("Running command and waiting for it to finish...")
	err := cmd.Run()
	log.Printf("Command finished with error: %v", err)
	stdout := outbuf.String()
	return stdout
}

func StoreRelationship( parent, child string ) {

	// write to file.
	//parent = strings.Replace(parent, "section/", "section-", -1)
	//child = strings.Replace(child, "section/", "section-", -1)

	if directory != "" {
		if parent == "" {return}
	}


	relation := "\"" + child + "\"" + " -> " + "\"" + parent + "\"" + "\n"
	relation = strings.Replace( relation, ".oet", "", -1)
	fmt.Println( relation )
	//relationfile.WriteString(relation )
	relationset = append(relationset, relation)
}

func FindParentTemplates( id string, file string) {
	// find names of templates that contain id

	if( id == "") {
		log.Printf( "failure....no id")
		return
		//log.Panic( "failure....no id")
	}

	var foundfiles string = GrepDir(id)

	log.Printf("FindParentTemplates( " + id + ") = (" + foundfiles +")")

	results := strings.Split(foundfiles, "\n")

	for i:= range results {
		result := results[i]
		parts := strings.Split(result,":")
		parent :=  parts[0]
		parent = strings.TrimSpace(parent)

//		if (parent != "" ) || (directory != "") {
		if parent != ""  {
				fmt.Println("parent - " + parent)
				StoreRelationship(directory + "/" +parent, file)
				id = FindRootTemplateID( parent )
				FindParentTemplates( id, directory + "/" +parent)
			}


	}
}

func removeDuplicates(elements []string) []string {
	// Use map to record duplicates as we find them.
	encountered := map[string]bool{}
	result := []string{}

	for v := range elements {
		if encountered[elements[v]] == true {
			// Do not add duplicate.
		} else {
			// Record this element as an encountered element.
			encountered[elements[v]] = true
			// Append to result slice.
			result = append(result, elements[v])
		}
	}
	// Return the new slice.
	return result
}

func findAllOETfiles(path string) string {

	cmd := exec.Command("find", path)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	log.Printf("Running command and waiting for it to finish...")
	err := cmd.Run()
	log.Printf("Command finished with error: %v", err)
	stdout := outbuf.String()
	return stdout

}

func processAllOETfiles( allfileslist string ){

	results := strings.Split(allfileslist, "\n")

	for i:= range results {
		aFile := results[i]
		aFile = strings.TrimSpace(aFile)
		if strings.HasSuffix( aFile,".oet" )  {
			template_id := FindRootTemplateID( aFile)
			log.Printf(template_id)
			FindParentTemplates(template_id, aFile)
		}
	}

}
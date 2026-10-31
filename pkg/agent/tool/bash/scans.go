package bash

import (
	"strings"

	"loop/pkg/domain/ignore"
)

// WithScanDefaults gives common shell scanners the same dependency exclusions
// as native search tools. This is a traversal default, not a shell sandbox:
// builds and package managers can still execute installed dependencies.
func WithScanDefaults(command, workspace string) string {
	names := ignore.DirectoryNames()
	var prelude strings.Builder
	prelude.WriteString("_loop_scan_root=" + ShellQuote(workspace) + "\n")
	prelude.WriteString("_loop_scan_allowed() {\n local resolved\n resolved=$(cd -- \"$1\" 2>/dev/null && pwd -P) || return 0\n [[ $resolved == \"$_loop_scan_root\" ]] && return 0\n resolved=${resolved#\"$_loop_scan_root\"/}\n case /${resolved,,}/ in\n")
	for i, name := range names {
		if i > 0 {
			prelude.WriteByte('|')
		}
		prelude.WriteString("*/" + name + "/*")
	}
	prelude.WriteString(") printf 'scan skipped: dependency or generated directory: %s\\n' \"$1\" >&2; return 2;;\n esac\n}\n")
	prelude.WriteString(`find() {
 local -a options=() roots=() expression=() printing=(-print)
 local arg skip=0 in_exec=0
 while (( $# )); do
  case "$1" in
   -H|-L|-P|-O[0-3]) options+=("$1"); shift;;
   -D) if (( $# < 2 )); then command find "$@"; return; fi; options+=("$1" "$2"); shift 2;;
   --) shift; break;;
   --help|-help|--version|-version) command find "$@"; return;;
   *) break;;
  esac
 done
 while (( $# )); do
  case "$1" in -[a-zA-Z]*|'!'|'(') break;; esac
  _loop_scan_allowed "$1" || return
  roots+=("$1"); shift
 done
 if (( ${#roots[@]} == 0 )); then
  _loop_scan_allowed . || return
  roots=(.)
 fi
 expression=("$@")
 if (( ${#expression[@]} == 0 )); then expression=(-print); fi
 for arg in "${expression[@]}"; do
  if (( in_exec )); then
   if [[ $arg == ';' || $arg == '+' ]]; then in_exec=0; fi
   continue
  fi
  if (( skip )); then skip=$((skip-1)); continue; fi
  case "$arg" in
   -depth|-delete|-files0-from) printf 'Use native file tools for traversal with %s; dependency pruning must remain enabled.\n' "$arg" >&2; return 2;;
   -name|-iname|-path|-ipath|-wholename|-iwholename|-regex|-iregex|-type|-xtype|-maxdepth|-mindepth|-mtime|-atime|-ctime|-mmin|-amin|-cmin|-newer|-anewer|-cnewer|-user|-group|-uid|-gid|-size|-perm|-links|-inum|-fstype|-regextype) skip=1;;
   -exec|-execdir|-ok|-okdir) printing=();in_exec=1;;
   -printf|-fprint|-fprint0|-fls) printing=();skip=1;;
   -fprintf) printing=();skip=2;;
   -print|-print0|-ls|-quit) printing=();;
  esac
 done
 # Keep batched -exec at the top level: BusyBox does not flush batches
 # nested inside parentheses. Expressions without actions need grouped print.
 if (( ${#printing[@]} )); then expression=( \( "${expression[@]}" \) -print ); fi
 command find "${options[@]}" "${roots[@]}" -type d \( `)
	for i, name := range names {
		if i > 0 {
			prelude.WriteString(" -o ")
		}
		prelude.WriteString("-iname " + ShellQuote(name))
	}
	prelude.WriteString(" \\) -prune -o \"${expression[@]}\"\n}\n")
	for _, scanner := range []string{"grep", "rg", "fd", "fdfind", "tree", "du"} {
		if scanner == "grep" {
			prelude.WriteString(`grep() {
 local -a scan_args=()
 local scan_arg scan_flags scan_char scan_rebuilt scan_value=0 scan_options=1 scan_recursive=0
 for scan_arg in "$@"; do
  if (( scan_value )); then scan_value=0
  elif (( scan_options )); then
   case "$scan_arg" in
    --) scan_options=0;;
    --dereference-recursive|--recursive) scan_arg=--recursive;scan_recursive=1;;
    --regexp|--file|--max-count|--after-context|--before-context|--context|--directories|--devices|--include|--exclude|--exclude-from|--exclude-dir|--label|--binary-files) scan_value=1;;
    --*) ;;
    -*)
     scan_flags="${scan_arg:1}"; scan_rebuilt=-
     while [[ -n $scan_flags ]]; do
      scan_char="${scan_flags:0:1}";scan_flags="${scan_flags:1}"
      [[ $scan_char == R ]] && scan_char=r
      [[ $scan_char == r ]] && scan_recursive=1
      scan_rebuilt+="$scan_char"
      case "$scan_char" in
       e|f|m|A|B|C|d|D)
        [[ -z $scan_flags ]] && scan_value=1
        scan_rebuilt+="$scan_flags";break;;
      esac
     done
     scan_arg="$scan_rebuilt";;
   esac
  fi
  scan_args+=("$scan_arg")
 done
 if (( !scan_recursive )); then command grep "${scan_args[@]}"; return; fi
 if [[ $(command grep --help 2>&1) != *--exclude-dir* ]]; then
  printf 'Recursive shell grep cannot exclude dependency directories on this system; use the native grep tool.\n' >&2
  return 2
 fi
 command grep`)
		} else {
			if scanner == "du" {
				prelude.WriteString("du() { if [[ $(command du --help 2>&1) != *--exclude* ]]; then printf 'Shell du cannot exclude dependency directories on this system; use native file tools for source inspection.\\n' >&2; return 2; fi; command du")
			} else {
				prelude.WriteString(scanner + "() { command " + scanner)
			}
		}
		if scanner == "tree" {
			prelude.WriteString(" -I " + ShellQuote(strings.Join(names, "|")))
		} else {
			for _, name := range names {
				switch scanner {
				case "grep":
					prelude.WriteString(" --exclude-dir=" + ShellQuote(name))
				case "rg":
					prelude.WriteString(" --glob " + ShellQuote("!**/"+name+"/**"))
				case "fd", "fdfind":
					prelude.WriteString(" --exclude " + ShellQuote(name))
				case "du":
					prelude.WriteString(" --exclude=" + ShellQuote(name))
				}
			}
		}
		if scanner == "grep" {
			prelude.WriteString(" \"${scan_args[@]}\"; }\n")
		} else {
			prelude.WriteString(" \"$@\"; }\n")
		}
	}
	prelude.WriteString(command)
	return prelude.String()
}

Check that Mermaid diagrams render, with the same renderer the chat draws them with. Use it when you're unsure of a diagram's syntax, and after writing or editing a diagram in a file.

<usage>
- Pass `source` with one diagram, or `file_path` to check every top-level mermaid fence in a markdown file (a `.mmd` or `.mermaid` file is checked as one diagram)
- Returns whether each diagram renders, and for each one that doesn't, the line it fails on (in the file when `file_path` is given) and the parser's message
- Fix every failure and check again; a diagram that fails here shows as plain code in the chat. The renderer is Pele, which follows Mermaid closely; a failure marked "couldn't check" (such as a type it doesn't draw yet) may still be valid Mermaid
</usage>

<common_failures>
- A sequence diagram participant ID that is a keyword in any case, such as `end`, `off`, `over`, `note`, `opt`, `option`, `loop`, `alt`, `else`, `par`, `and`, `box`, `break`, `rect`, `critical`, `create` or `autonumber`. Rename the ID and keep the shown name with `participant Ofr as Off`
- Unquoted labels holding brackets, quotes or semicolons; wrap them in double quotes
- Lowercase `end` as a flowchart node ID
</common_failures>

package lib

Mode: "list" | "table" | "hidden"

// A reference inside lib, seen from the importer.
#Legend: {display: Mode}

#A: {kind: "a", x: int | *1}
#B: {kind: "b", y: int | *2}
#AorB: #A | #B

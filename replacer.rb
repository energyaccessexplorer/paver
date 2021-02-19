#!/usr/bin/env ruby

html = ARGV[1]
go = ARGV[0]

str = File.read(html)

tmpl = File.read(go)
result = tmpl.gsub('----REPLACE-ME----', str)

f = File.open(go, 'w')
f.write result
f.close

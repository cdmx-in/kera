import { BookOpen, Brain, Calendar, Code2, FileText, Globe, HelpCircle, Mail, Pencil, Puzzle, Search, Sheet, Sparkles, Terminal, Wrench, type LucideIcon } from 'lucide-react'
import type { ToolKind } from './ui-config'

// Icon names usable in toolDisplay / features / integrations config.
const byName: Record<string, LucideIcon> = {
  search: Search,
  globe: Globe,
  web: Globe,
  file: FileText,
  read: BookOpen,
  book: BookOpen,
  code: Code2,
  terminal: Terminal,
  brain: Brain,
  memory: Brain,
  pencil: Pencil,
  write: Pencil,
  help: HelpCircle,
  ask: HelpCircle,
  sheet: Sheet,
  calendar: Calendar,
  mail: Mail,
  sheets: Sheet,
  puzzle: Puzzle,
  sparkles: Sparkles,
  wrench: Wrench,
}

const byKind: Record<ToolKind, LucideIcon> = { search: Search, read: BookOpen, code: Terminal, ask: HelpCircle, write: Pencil, memory: Brain, other: Wrench }

export function iconFor(name?: string, kind?: ToolKind): LucideIcon {
  return (name && byName[name]) || byKind[kind ?? 'other'] || Wrench
}

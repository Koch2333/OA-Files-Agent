// The ONE place that decides which glyph a UI icon name renders.
// Call sites keep using Material-style names (<Icon name="folder_open" />); this table resolves them to
// Lucide components (https://lucide.dev, ISC). To switch icon sets later, change the imports and the values
// here (and nothing else): Icon.vue only needs a Vue component per name.
// Imports are static and named so Vite/Rollup tree-shakes everything that is not listed.
import type { Component } from 'vue'
import {
  Activity, Archive, ArchiveRestore, ArrowDown, ArrowLeft, ArrowLeftRight, ArrowRight, ArrowUp, Ban, BadgeCheck, Bot,
  Building2, CalendarDays, Check, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, Circle, CircleAlert, CircleCheck,
  CircleCheckBig, CircleHelp, CircleStop, CircleX, ClipboardCheck, ClipboardClock, Clock, Cloud, CloudCog, CloudOff,
  CloudUpload, Cloudy, Contrast, Copy, Database, Download, Ellipsis, Eye, EyeOff, ExternalLink, File, FileCog,
  FileImage, FilePen, FileSearch, FilePlus, FileSpreadsheet, FileText, FileType, FileUp, Fingerprint, Folder, FolderCog,
  FolderInput, FolderOpen, FolderPlus, FolderSymlink, FolderTree, FolderX, Funnel, Gauge, HardDrive, HardDriveUpload,
  History, Hourglass, House, ImageOff, Inbox, Info, Key, KeyRound, LayoutDashboard, Link, List, ListChecks,
  LoaderCircle, Lock, LockKeyhole, LockOpen, LogIn, LogOut, Menu, MessagesSquare, Minus, MonitorSmartphone, Moon,
  Move, Network, Pencil, Play, Plus, QrCode, RectangleEllipsis, RefreshCw, RotateCcw, Rows3, Save, ScrollText,
  Search, Send, Server, Settings, ShieldAlert, ShieldCheck, Smartphone, Sparkles, Sun, SunMoon, SlidersHorizontal,
  Table2, Tag, Terminal, Trash2, TriangleAlert, Undo2, Unlink, Upload, User, UserCog, UserPlus, Users, WandSparkles,
  Waypoints, Wifi, Wrench, X, Zap, ZapOff, FlaskConical, Heading, Scissors,
} from 'lucide-vue-next'

export const FALLBACK_ICON: Component = Circle

export const ICON_MAP: Record<string, Component> = {
  // --- navigation (navModel.ts) ---
  space_dashboard: LayoutDashboard,
  dashboard: LayoutDashboard,
  upload_file: FileUp,
  smart_toy: Bot,
  task_alt: CircleCheckBig,
  folder_open: FolderOpen,
  table_view: Table2,
  folder_managed: FolderCog,
  rule_settings: FileCog,
  cloud_sync: CloudCog,
  settings: Settings,
  group: Users,
  shield_lock: ShieldCheck,
  devices: MonitorSmartphone,
  history: History,
  monitor_heart: Activity,
  home: House,
  menu: Menu,

  // --- status / feedback ---
  check: Check,
  check_circle: CircleCheck,
  cancel: CircleX,
  error: CircleAlert,
  warning: TriangleAlert,
  info: Info,
  help: CircleHelp,
  priority_high: CircleAlert,
  block: Ban,
  progress_activity: LoaderCircle,
  sync: RefreshCw,
  autorenew: RefreshCw,
  refresh: RefreshCw,
  schedule: Clock,
  hourglass_top: Hourglass,
  pending_actions: ClipboardClock,
  verified: BadgeCheck,
  verified_user: ShieldCheck,
  gpp_bad: ShieldAlert,
  privacy_tip: ShieldAlert,
  circle: Circle,

  // --- actions ---
  add: Plus,
  remove: Minus,
  close: X,
  edit: Pencil,
  delete: Trash2,
  delete_sweep: Trash2,
  restore_from_trash: ArchiveRestore,
  content_copy: Copy,
  content_cut: Scissors,
  save: Save,
  send: Send,
  undo: Undo2,
  replay: RotateCcw,
  play_arrow: Play,
  stop_circle: CircleStop,
  search: Search,
  filter: Funnel,
  tune: SlidersHorizontal,
  download: Download,
  downloading: Download,
  upload: Upload,
  open_in_new: ExternalLink,
  link: Link,
  link_off: Unlink,
  visibility: Eye,
  visibility_off: EyeOff,
  preview: Eye,
  auto_fix: WandSparkles,
  auto_awesome: Sparkles,
  bolt: Zap,
  offline_bolt: ZapOff,
  build: Wrench,
  label: Tag,
  science: FlaskConical,
  network_check: Wifi,
  rule: ListChecks,
  fact_check: ClipboardCheck,
  inventory_2: Archive,
  logout: LogOut,
  login: LogIn,
  move: Move,
  sync_alt: ArrowLeftRight,
  more_horiz: Ellipsis,

  // --- arrows / disclosure ---
  arrow_back: ArrowLeft,
  arrow_forward: ArrowRight,
  arrow_upward: ArrowUp,
  arrow_downward: ArrowDown,
  arrow_drop_down: ChevronDown,
  expand_more: ChevronDown,
  expand_less: ChevronUp,
  chevron_left: ChevronLeft,
  chevron_right: ChevronRight,

  // --- files and folders ---
  folder: Folder,
  create_new_folder: FolderPlus,
  drive_file_move: FolderInput,
  folder_off: FolderX,
  folder_shared: FolderSymlink,
  account_tree: FolderTree,
  note_add: FilePlus,
  manage_search: FileSearch,
  description: FileText,
  draft: File,
  picture_as_pdf: FileType,
  image: FileImage,
  broken_image: ImageOff,
  table_chart: FileSpreadsheet,
  drive_file_rename_outline: FilePen,
  inbox: Inbox,
  event: CalendarDays,
  list: List,
  table_rows: Rows3,
  view_agenda: Rows3,
  title: Heading,

  // --- storage providers (utils/storage.ts KIND_ICON) ---
  cloud: Cloud,
  cloud_circle: Cloudy,
  cloud_off: CloudOff,
  cloud_upload: CloudUpload,
  lan: Network,
  terminal: Terminal,
  dns: Server,
  storage: Database,
  forum: MessagesSquare,
  hub: Waypoints,
  hard_drive: HardDrive,
  add_to_drive: HardDriveUpload,
  domain: Building2,

  // --- accounts and security ---
  person: User,
  person_add: UserPlus,
  manage_accounts: UserCog,
  lock: Lock,
  lock_open: LockOpen,
  lock_clock: LockKeyhole,
  key: Key,
  password: RectangleEllipsis,
  passkey: KeyRound,
  fingerprint: Fingerprint,
  qr_code_2: QrCode,
  mobile_off: Smartphone,
  phonelink_erase: Smartphone,

  // --- theme / density / misc ---
  contrast: Contrast,
  brightness_auto: SunMoon,
  light_mode: Sun,
  dark_mode: Moon,
  density_small: Rows3,
  policy: ScrollText,
  speed: Gauge,
}

// Semantic aliases: any of these may be used instead of a Material name.
Object.assign(ICON_MAP, {
  'file-upload': FileUp,
  'file-text': FileText,
  trash: Trash2,
  'sparkles': Sparkles,
  spinner: LoaderCircle,
  loading: LoaderCircle,
  success: CircleCheck,
  failure: CircleX,
  attention: CircleAlert,
  ai: Sparkles,
  archive: Archive,
  tasks: CircleCheckBig,
  ledger: Table2,
  files: FolderOpen,
  security: ShieldCheck,
  diagnostics: Activity,
})

const warned = new Set<string>()

/** Resolves a UI icon name to a component; unknown names get the neutral fallback (and one dev-only warning). */
export function resolveIcon(name: string): Component {
  const c = ICON_MAP[name]
  if (c) return c
  if (import.meta.env?.DEV && !warned.has(name)) {
    warned.add(name)
    console.warn(`[Icon] "${name}" has no entry in components/iconMap.ts; using the fallback icon`)
  }
  return FALLBACK_ICON
}

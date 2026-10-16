/** Global API injected by /theme-sdk.js into an isolated frontend document. */
export interface ThemeManifest {
  id: string;
  name: string;
  type?: 'skin' | 'addon';
  for?: string;
  frontend?: {entry: string; api_version: 1};
  settings?: Array<{id: string; type: 'toggle' | 'choice' | 'addons'; default?: string}>;
  options?: Record<string, string | string[]>;
  decorations?: Array<{id: string; name: string; nodes?: string[]; regions?: string[]; default?: boolean; images?: Record<string, string>; vars?: Record<string, string>}>;
}
export interface FrontendContext {
  api_version: 1;
  theme: ThemeManifest;
  themes: ThemeManifest[];
  brand: {name?: string; logo?: string};
  mode: 'light' | 'dark';
  preference: 'auto' | 'light' | 'dark';
  route: string;
  adminURL: '/admin';
}
export interface FrontendAPI {
  readonly version: 1;
  readonly ready: Promise<FrontendContext>;
  readonly context: FrontendContext | undefined;
  api<T = unknown>(path: string): Promise<T>;
  onContext(callback: (context: FrontendContext) => void): () => void;
  navigate(path: string): Promise<void>;
  setSkin(id: string): Promise<void>;
  setMode(mode: 'auto' | 'light' | 'dark'): Promise<void>;
  openAdmin(): Promise<void>;
  storage: {get<T = unknown>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>};
  reportError(error: unknown): void;
}
declare global {
  const TZA: FrontendAPI;
  interface Window {readonly TZA: FrontendAPI}
}

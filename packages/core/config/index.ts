import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

interface ConfigState {
  cdnDomain: string;
  allowSignup: boolean;
  googleClientId: string;
  excalidrawRoomUrl: string;
  setCdnDomain: (domain: string) => void;
  setAuthConfig: (config: { allowSignup: boolean; googleClientId?: string }) => void;
  setExcalidrawRoomUrl: (url: string) => void;
}

export const configStore = createStore<ConfigState>((set) => ({
  cdnDomain: "",
  allowSignup: true,
  googleClientId: "",
  excalidrawRoomUrl: "",
  setCdnDomain: (domain) => set({ cdnDomain: domain }),
  setAuthConfig: ({ allowSignup, googleClientId = "" }) =>
    set({ allowSignup, googleClientId }),
  setExcalidrawRoomUrl: (url) => set({ excalidrawRoomUrl: url }),
}));

export function useConfigStore(): ConfigState;
export function useConfigStore<T>(selector: (state: ConfigState) => T): T;
export function useConfigStore<T>(selector?: (state: ConfigState) => T) {
  return useStore(configStore, selector as (state: ConfigState) => T);
}

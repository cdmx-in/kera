// @novnc/novnc ships no types; this covers what DesktopPanel uses.
declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string)
    scaleViewport: boolean
    viewOnly: boolean
    disconnect(): void
  }
}

export namespace adb {
	
	export class Device {
	    serial: string;
	    state: string;
	
	    static createFrom(source: any = {}) {
	        return new Device(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serial = source["serial"];
	        this.state = source["state"];
	    }
	}
	export class DeviceDetail {
	    serial: string;
	    state: string;
	    transport: string;
	    model: string;
	    version: string;
	    brand: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serial = source["serial"];
	        this.state = source["state"];
	        this.transport = source["transport"];
	        this.model = source["model"];
	        this.version = source["version"];
	        this.brand = source["brand"];
	    }
	}
	export class HistoryEntry {
	    serial: string;
	    // Go type: time
	    lastSeen: any;
	    // Go type: time
	    disconnectedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serial = source["serial"];
	        this.lastSeen = this.convertValues(source["lastSeen"], null);
	        this.disconnectedAt = this.convertValues(source["disconnectedAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class RecordState {
	    recording: boolean;
	    elapsed: number;
	    phase: string;
	    phaseDetail?: string;
	
	    static createFrom(source: any = {}) {
	        return new RecordState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.recording = source["recording"];
	        this.elapsed = source["elapsed"];
	        this.phase = source["phase"];
	        this.phaseDetail = source["phaseDetail"];
	    }
	}

}


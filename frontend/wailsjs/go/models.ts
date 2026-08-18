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

}


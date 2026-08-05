(() => {
                const fp = __FP_PAYLOAD__;
                const defineGetter = (obj, prop, value) => {
                    try { Object.defineProperty(obj, prop, { get: () => value, configurable: true }); } catch (_) {}
                };
                defineGetter(Navigator.prototype, 'webdriver', undefined);
                defineGetter(Navigator.prototype, 'platform', fp.platform);
                defineGetter(Navigator.prototype, 'vendor', fp.vendor);
                defineGetter(Navigator.prototype, 'language', fp.languages[0]);
                defineGetter(Navigator.prototype, 'languages', fp.languages);
                defineGetter(Navigator.prototype, 'hardwareConcurrency', fp.hardwareConcurrency);
                defineGetter(Navigator.prototype, 'deviceMemory', fp.deviceMemory);
                defineGetter(Navigator.prototype, 'maxTouchPoints', fp.maxTouchPoints);
                defineGetter(Screen.prototype, 'width', fp.screenWidth);
                defineGetter(Screen.prototype, 'height', fp.screenHeight);
                defineGetter(Screen.prototype, 'availWidth', fp.screenWidth);
                defineGetter(Screen.prototype, 'availHeight', fp.screenHeight - 40);
                defineGetter(window, 'outerWidth', fp.outerWidth);
                defineGetter(window, 'outerHeight', fp.outerHeight);
                defineGetter(window, 'devicePixelRatio', fp.deviceScaleFactor);
                if (!navigator.userAgentData) {
                    defineGetter(Navigator.prototype, 'userAgentData', {
                        mobile: false,
                        platform: 'Windows',
                        brands: [
                            { brand: 'Google Chrome', version: fp.chromeMajor },
                            { brand: 'Chromium', version: fp.chromeMajor },
                            { brand: 'Not.A/Brand', version: '24' },
                        ],
                        getHighEntropyValues: async hints => {
                            const values = {
                                architecture: 'x86', bitness: '64', mobile: false, model: '',
                                platform: 'Windows', platformVersion: '15.0.0', uaFullVersion: fp.chromeFull,
                                fullVersionList: [
                                    { brand: 'Google Chrome', version: fp.chromeFull },
                                    { brand: 'Chromium', version: fp.chromeFull },
                                    { brand: 'Not.A/Brand', version: '24.0.0.0' },
                                ],
                                wow64: false,
                            };
                            return Object.fromEntries(hints.filter(h => h in values).map(h => [h, values[h]]));
                        },
                    });
                }
                try {
                    const originalQuery = navigator.permissions && navigator.permissions.query;
                    if (originalQuery) {
                        navigator.permissions.query = params => {
                            if (params && params.name === 'notifications') {
                                return Promise.resolve({ state: Notification.permission });
                            }
                            if (params && (params.name === 'microphone' || params.name === 'camera')) {
                                return Promise.resolve({ state: 'denied' });
                            }
                            return originalQuery.call(navigator.permissions, params);
                        };
                    }
                } catch (_) {}
                try {
                    const deniedMedia = () => Promise.reject(new DOMException('Permission denied', 'NotAllowedError'));
                    if (navigator.mediaDevices) {
                        navigator.mediaDevices.getUserMedia = deniedMedia;
                    } else {
                        defineGetter(Navigator.prototype, 'mediaDevices', { getUserMedia: deniedMedia });
                    }
                } catch (_) {}
                try {
                    const getParameter = WebGLRenderingContext.prototype.getParameter;
                    WebGLRenderingContext.prototype.getParameter = function(parameter) {
                        if (parameter === 37445) return 'Intel Inc.';
                        if (parameter === 37446) return 'Intel Iris OpenGL Engine';
                        return getParameter.call(this, parameter);
                    };
                } catch (_) {}
            })();

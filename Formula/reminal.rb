class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.7.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_darwin_arm64.tar.gz"
      sha256 "7ac93ebf484d0678ada3757ae85cee282ca3dbbc14f52d83cef97b64b8125788"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_darwin_amd64.tar.gz"
      sha256 "ef59464de3e74db5941553778c7926478d938347ad859c40ce89f7a9b9960c45"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_linux_arm64.tar.gz"
      sha256 "76bd2fda8e6bed73adcae9ff1861caedba75810fbaa9c6536a1555cfe7b35f3c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.7.0/reminal_1.7.0_linux_amd64.tar.gz"
      sha256 "7abb682920b956d750f2e0a405a1936ed5594c5525b681ac43d810752cee8095"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end

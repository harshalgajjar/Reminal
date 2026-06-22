class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.7"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.7/reminal_0.3.7_darwin_arm64.tar.gz"
      sha256 "01b11e00ef6b5cf25e4b78c7f2bca174dee0b427a9cf5ea9a474b3de38588f3b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.7/reminal_0.3.7_darwin_amd64.tar.gz"
      sha256 "29f9ce01dc01a4f9029ffa040d99fc88aba641e3f6d653e23e45a42d2e746dcc"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.7/reminal_0.3.7_linux_arm64.tar.gz"
      sha256 "733fcbb97d97a1f1b3bd68500d7b4d7da7a1566f35898f7c0ca12dd9fc02d686"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
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
